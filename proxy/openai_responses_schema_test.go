package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

const proxyObservedWorkflowPattern = `^[^\p{Cc}\p{Zl}\p{Zp}"]*$`

func collectSchemaPatterns(value any, patterns *[]string) {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if key == "pattern" {
				if pattern, ok := child.(string); ok {
					*patterns = append(*patterns, pattern)
				}
			}
			collectSchemaPatterns(child, patterns)
		}
	case []any:
		for _, child := range item {
			collectSchemaPatterns(child, patterns)
		}
	}
}

func workflowPatterns(t *testing.T, body []byte) []string {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode converted request: %v", err)
	}
	var patterns []string
	collectSchemaPatterns(decoded, &patterns)
	return patterns
}

func TestOpenAIResponsesSchemaNormalizationUsesActualUpstreamURL(t *testing.T) {
	const kiroTool = `[{"toolSpecification":{"name":"run_workflow","inputSchema":{"json":{"type":"object","properties":{"name":{"type":"string","pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$"}}}}}}]`
	rawTools := []json.RawMessage{}
	if err := json.Unmarshal([]byte(kiroTool), &rawTools); err != nil {
		t.Fatal(err)
	}
	conv := &converter.ParsedConversation{
		ModelID: "gpt-5.6", SystemText: "stable system", CurrentContent: "test",
		RawTools: rawTools,
	}
	pre := &Preflight{Protocol: "kiro", Conv: conv}

	localUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("local upstream path = %q", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read captured request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var decoded any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode captured request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var patterns []string
		collectSchemaPatterns(decoded, &patterns)
		if len(patterns) != 1 || patterns[0] == proxyObservedWorkflowPattern {
			t.Errorf("local upstream received unnormalized schema: %q", patterns)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, pattern := range patterns {
			if _, err := regexp.Compile(pattern); err != nil {
				t.Errorf("local upstream cannot compile pattern %q: %v", pattern, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer localUpstream.Close()

	for _, tc := range []struct {
		name, provider, baseSuffix, endpoint, protocol string
		disableAutoSuffix, wantNormalized              bool
	}{
		{name: "OpenAI Responses actual URL", provider: "openai", baseSuffix: "/v1", endpoint: "/responses", protocol: "kiro", wantNormalized: true},
		{name: "DisableAutoSuffix custom actual URL", provider: "openai", baseSuffix: "/custom", endpoint: "/responses", protocol: "kiro", disableAutoSuffix: true},
		{name: "DisableAutoSuffix Responses actual URL", provider: "openai", baseSuffix: "/responses", endpoint: "/responses", protocol: "kiro", disableAutoSuffix: true, wantNormalized: true},
		{name: "OpenAI Chat", provider: "openai", baseSuffix: "/v1", endpoint: "/chat/completions", protocol: "kiro"},
		{name: "Codex Responses", provider: "codex", baseSuffix: "/v1", endpoint: "/responses", protocol: "kiro"},
		{name: "Anthropic", provider: "anthropic", baseSuffix: "/v1", endpoint: "/messages", protocol: "kiro"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &store.Model{
				Provider: tc.provider, BaseURL: localUpstream.URL + tc.baseSuffix,
				Endpoint: tc.endpoint, ModelID: "gpt-5.6", DisableAutoSuffix: tc.disableAutoSuffix,
			}
			testPre := pre
			if tc.protocol != pre.Protocol {
				testPre = &Preflight{Protocol: tc.protocol, Body: []byte(`{"model":"old","input":"pass through"}`)}
			}
			body, upstreamURL, err := convertRequestWithProtocol(testPre, model, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			patterns := workflowPatterns(t, body)
			if len(patterns) != 1 {
				t.Fatalf("expected one workflow schema pattern, got %q in %s", patterns, body)
			}
			wasNormalized := patterns[0] != proxyObservedWorkflowPattern
			if wasNormalized != tc.wantNormalized {
				t.Fatalf("actual URL gate mismatch: URL=%q normalized=%v pattern=%q", upstreamURL, wasNormalized, patterns[0])
			}
			if !wasNormalized && patterns[0] != proxyObservedWorkflowPattern {
				t.Fatalf("non-target path changed pattern: %q", patterns[0])
			}
			if tc.wantNormalized && !strings.Contains(upstreamURL, "/responses") {
				t.Fatalf("test expected an actual Responses URL, got %q", upstreamURL)
			}
			if tc.name == "OpenAI Responses actual URL" {
				req, err := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("send to local mock upstream: %v", err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("mock upstream rejected final request: status=%d", resp.StatusCode)
				}
			}
		})
	}

	passThrough := &Preflight{Protocol: "openai", Body: []byte(`{"model":"old","input":"pass through","tools":[{"parameters":{"pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$"}}]}`)}
	model := &store.Model{Provider: "openai", BaseURL: localUpstream.URL + "/v1", Endpoint: "/responses", ModelID: "gpt-5.6"}
	body, _, err := convertRequestWithProtocol(passThrough, model, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	patterns := workflowPatterns(t, body)
	if len(patterns) != 1 || patterns[0] != proxyObservedWorkflowPattern {
		t.Fatalf("non-Kiro raw passthrough changed schema pattern: patterns=%q body=%s", patterns, body)
	}
}

func TestOpenAIResponsesRequestPreservesSchemaIntegerPrecision(t *testing.T) {
	const preciseNumberTool = `{"toolSpecification":{"name":"run_workflow","inputSchema":{"json":{"type":"object","properties":{"name":{"type":"string","pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$"},"revision":{"type":"integer","default":9007199254740993}}}}}}`
	type rawKiroTool struct {
		ToolSpecification struct {
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			InputSchema struct {
				JSON json.RawMessage `json:"json"`
			} `json:"inputSchema"`
		} `json:"toolSpecification"`
	}
	rawTools := []json.RawMessage{json.RawMessage(preciseNumberTool)}
	pre := &Preflight{Protocol: "kiro", Conv: &converter.ParsedConversation{
		ModelID: "gpt-5.6", SystemText: "stable system", CurrentContent: "test", RawTools: rawTools,
	}}

	for _, continuation := range []bool{false, true} {
		name := "ordinary"
		if continuation {
			name = "continuation"
		}
		t.Run(name, func(t *testing.T) {
			model := &store.Model{
				Provider: "openai", BaseURL: "https://provider.example/v1", Endpoint: "/responses",
				ModelID: "gpt-5.6", EnableContinuation: continuation,
			}
			body, upstreamURL, err := convertRequestWithProtocol(pre, model, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(upstreamURL, "/responses") {
				t.Fatalf("unexpected upstream URL: %q", upstreamURL)
			}

			var request struct {
				Tools          []json.RawMessage `json:"tools"`
				PromptCacheKey string            `json:"prompt_cache_key"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			if len(request.Tools) != 1 {
				t.Fatalf("unexpected outbound tools: %+v", request.Tools)
			}
			var outboundTool struct {
				Type        string          `json:"type"`
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			}
			if err := json.Unmarshal(request.Tools[0], &outboundTool); err != nil {
				t.Fatal(err)
			}
			if outboundTool.Type != "function" || outboundTool.Name != "run_workflow" {
				t.Fatalf("unexpected outbound tool: %+v", outboundTool)
			}
			bodyTool := converter.OpenAITool{
				Type: outboundTool.Type,
				Function: converter.OpenAIFuncDef{
					Name: outboundTool.Name, Description: outboundTool.Description, Parameters: outboundTool.Parameters,
				},
			}
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(bodyTool.Function.Parameters.(json.RawMessage), &schema); err != nil {
				t.Fatal(err)
			}
			var revision map[string]json.RawMessage
			if err := json.Unmarshal(schema.Properties["revision"], &revision); err != nil {
				t.Fatal(err)
			}
			if got := string(revision["default"]); got != "9007199254740993" {
				t.Fatalf("outbound schema default lost integer precision: got %s, body=%s", got, body)
			}

			providerHash, report := converter.CachedOpenAIResponsesToolAudit(pre.Conv.ToolsHash, pre.Conv.RawTools)
			// 用 RawMessage 重建最终工具，避免测试自身把整数解码成 float64。
			bodyToolEnvelope := rawKiroTool{}
			bodyToolEnvelope.ToolSpecification.Name = bodyTool.Function.Name
			bodyToolEnvelope.ToolSpecification.Description = bodyTool.Function.Description
			bodyToolEnvelope.ToolSpecification.InputSchema.JSON = bodyTool.Function.Parameters.(json.RawMessage)
			bodyToolKiro, err := json.Marshal(bodyToolEnvelope)
			if err != nil {
				t.Fatal(err)
			}
			bodyProviderHash, bodyReport := converter.CachedOpenAIResponsesToolAudit("", []json.RawMessage{bodyToolKiro})
			if providerHash != bodyProviderHash || report.ProviderTools != bodyReport.ProviderTools {
				t.Fatalf("body tools differ from normalized provider audit: hash=%q body_hash=%q report=%+v body_report=%+v", providerHash, bodyProviderHash, report, bodyReport)
			}
			wantCacheKey := converter.PromptCacheKeyForAuditWithProviderToolsHash(pre.Conv, model.ModelID, "responses", providerHash)
			if report.ProviderTools != 1 || request.PromptCacheKey != wantCacheKey {
				t.Fatalf("final body cache key differs from normalized provider tool audit: report=%+v key=%q want=%q", report, request.PromptCacheKey, wantCacheKey)
			}
		})
	}
}
