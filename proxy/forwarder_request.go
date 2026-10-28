package proxy

import (
	"bytes"
	"fmt"
	"io"
	"kiro-proxy/codex"
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"net/url"
	"strings"

	json "github.com/goccy/go-json"
)

// useResponsesAPI 是请求/响应两侧唯一的"是否走 Responses API"判定口径。
// classifier（simple-task）请求必须走 chat/completions（上游返回可被分类器解析的纯文本），
// 绝不能走 responses。请求侧与响应侧必须使用同一判定，否则会出现请求按 chat 发、
// 响应却按 responses 解析（或反之）导致内容整段丢失。
func useResponsesAPI(pre *Preflight, m *store.Model) bool {
	if pre.Protocol != "kiro" || pre.Conv == nil {
		return false
	}
	if m.Provider == "anthropic" || m.Provider == "gemini" || m.Provider == "codex" {
		return false
	}
	if pre.Conv.IsClassifier {
		return false
	}
	return strings.Contains(m.Endpoint, "/responses")
}

// 与请求转换共用同一协议判定，避免自定义 URL 或 classifier 改写误导响应观测。
func upstreamContentProtocolForCandidate(pre *Preflight, model *store.Model, forceChat bool) contentMetricProtocol {
	switch model.Provider {
	case "anthropic":
		return contentMetricProtocolAnthropic
	case "gemini":
		return contentMetricProtocolGemini
	case "codex":
		return contentMetricProtocolResponses
	}
	if forceChat && model.Provider == "openai" {
		return contentMetricProtocolChat
	}
	if pre.Protocol == "kiro" && pre.Conv != nil {
		if useResponsesAPI(pre, model) {
			return contentMetricProtocolResponses
		}
		return contentMetricProtocolChat
	}
	if strings.Contains(strings.TrimSpace(model.Endpoint), "/responses") {
		return contentMetricProtocolResponses
	}
	return contentMetricProtocolChat
}

func buildUpstreamURL(baseURL, endpoint string) string {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}
	return strings.TrimRight(baseURL, "/") + endpoint
}

func resolveReasoningEffort(pre *Preflight, model *store.Model) string {
	if preIsAutoReasoningRequest(pre) && !pre.IsClassifier && pre.AutoReasoningEffort != nil {
		return *pre.AutoReasoningEffort
	}
	if pre != nil && pre.Conv != nil && !pre.IsClassifier {
		if pre.Conv.ReasoningEffort != "" {
			return pre.Conv.ReasoningEffort
		}
		if pre.GroupReasoningEffort != "" {
			return pre.GroupReasoningEffort
		}
	}
	if model == nil {
		return ""
	}
	defaultEffort, _ := converter.NormalizeReasoningEffort(model.ReasoningEffort)
	return defaultEffort
}

// preIsAutoReasoningRequest keeps Auto's duty-fixed effort scoped to requests
// that actually entered the Auto domain.  A manual/parent-model workflow can
// still carry a route object or stale metadata from an Auto-capable path, but
// its node-level request effort must remain authoritative.
func preIsAutoReasoningRequest(pre *Preflight) bool {
	if pre == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(pre.SourceModel), "auto") {
		return true
	}
	return pre.Conv != nil && strings.EqualFold(strings.TrimSpace(pre.Conv.ModelID), "auto")
}

// convertRequest 将请求体转换为上游格式
func convertRequest(pre *Preflight, m *store.Model) ([]byte, string, error) {
	return convertRequestWithProtocol(pre, m, nil, false)
}

func convertRequestWithProtocol(pre *Preflight, m *store.Model, reasoningHistory map[string]converter.ReasoningTrace, forceChat bool) ([]byte, string, error) {
	baseURL := strings.TrimRight(m.BaseURL, "/")
	endpoint := strings.TrimSpace(m.Endpoint)
	if endpoint == "" {
		return nil, "", fmt.Errorf("接口端点不能为空，请在模型配置中完整填写端点路径")
	}
	upstreamURL := buildUpstreamURL(baseURL, endpoint)
	if m.DisableAutoSuffix {
		upstreamURL = strings.TrimSpace(m.BaseURL)
	}

	if pre.Protocol != "kiro" || pre.Conv == nil {
		return replaceModelInBody(pre.Body, m.ModelID), upstreamURL, nil
	}
	conv := pre.Conv
	if len(reasoningHistory) > 0 {
		conv = converter.WithReasoningHistory(conv, reasoningHistory)
	}

	reasoningEffort := resolveReasoningEffort(pre, m)
	convertChat := func() ([]byte, error) {
		if isOpenAIChatUpstream(m, upstreamURL) {
			return converter.KiroToOpenAIChatUpstream(conv, m.ModelID, reasoningEffort)
		}
		return converter.KiroToOpenAI(conv, m.ModelID, reasoningEffort)
	}

	// Kiro 协议：根据 provider + endpoint 选择转换器
	var body []byte
	var err error
	switch {
	case forceChat && m.Provider == "openai":
		upstreamURL = responsesToChatURL(upstreamURL)
		body, err = convertChat()
	case m.Provider == "anthropic":
		body, err = converter.KiroToAnthropic(conv, m.ModelID, reasoningEffort)
	case m.Provider == "gemini":
		body, err = converter.KiroToGemini(conv, m.ModelID, reasoningEffort)
		if !m.DisableAutoSuffix {
			// Gemini URL: 替换 {model} 占位符，追加 ?alt=sse
			upstreamURL = strings.Replace(upstreamURL, "{model}", m.ModelID, 1)
			if !strings.Contains(upstreamURL, "alt=sse") {
				if strings.Contains(upstreamURL, "?") {
					upstreamURL += "&alt=sse"
				} else {
					upstreamURL += "?alt=sse"
				}
			}
		}
	case m.Provider == "codex":
		body, err = converter.KiroToResponses(conv, m.ModelID, reasoningEffort)
		if err == nil {
			body = codex.NewRequestBodyFromResponses(body, m.ServiceTier)
		}
	case useResponsesAPI(pre, m):
		if isOpenAIResponsesUpstream(m, upstreamURL) {
			body, err = converter.KiroToOpenAIResponsesUpstream(conv, m.ModelID, reasoningEffort, m.EnableContinuation)
		} else if m.EnableContinuation {
			body, err = converter.KiroToResponsesForContinuation(conv, m.ModelID, reasoningEffort)
		} else {
			body, err = converter.KiroToResponses(conv, m.ModelID, reasoningEffort)
		}
		if err == nil {
			body = setResponsesStore(body, m.EnableContinuation, isOpenAIResponsesUpstream(m, upstreamURL))
			body = applyConfiguredResponsesServiceTier(body, m)
		}
	default: // chat/completions，或 classifier 请求
		if !m.DisableAutoSuffix && strings.Contains(endpoint, "/responses") {
			// 配置成 responses，但本次走 chat（classifier）：替换端点路径
			chatEndpoint := strings.Replace(endpoint, "/responses", "/chat/completions", 1)
			upstreamURL = baseURL + chatEndpoint
		}
		body, err = convertChat()
	}
	return body, upstreamURL, err
}

func isOpenAIResponsesUpstream(model *store.Model, upstreamURL string) bool {
	if model == nil || model.Provider != "openai" {
		return false
	}
	parsed, err := url.Parse(upstreamURL)
	return err == nil && strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/responses")
}

func responsesToChatURL(upstreamURL string) string {
	parsed, err := url.Parse(upstreamURL)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		path := strings.TrimRight(parsed.Path, "/")
		if strings.HasSuffix(path, "/responses") {
			parsed.Path = strings.TrimSuffix(path, "/responses") + "/chat/completions"
		} else {
			parsed.Path = path + "/chat/completions"
		}
		parsed.RawPath = ""
		return parsed.String()
	}
	return strings.TrimRight(upstreamURL, "/") + "/chat/completions"
}

func replaceModelInBody(body []byte, newModel string) []byte {
	if newModel == "" || len(body) == 0 {
		return body
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body
	}
	parsed["model"] = newModel
	result, _ := json.Marshal(parsed)
	return result
}

func setResponsesStore(body []byte, store, preserveNumbers bool) []byte {
	var parsed map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if preserveNumbers {
		decoder.UseNumber()
	}
	if err := decoder.Decode(&parsed); err != nil {
		return body
	}
	// 与原 Unmarshal 一致，只接受一个完整的 JSON 文档。
	var trailing interface{}
	if decoder.Decode(&trailing) != io.EOF {
		return body
	}
	parsed["store"] = store
	result, err := json.Marshal(parsed)
	if err != nil {
		return body
	}
	return result
}

// applyConfiguredResponsesServiceTier applies the model's explicit FAST
// setting to a native OpenAI Responses request.  The setting is deliberately
// scoped to the top-level service_tier field: provider-specific Codex request
// normalization belongs to codex.NewRequestBodyFromResponses and must not be
// reused for ordinary OpenAI Responses routes.
//
// A configured fast tier only supplies the upstream priority tier when the
// client left the tier unset (or selected the neutral default).  Explicit
// tiers such as flex or priority are preserved verbatim.
func applyConfiguredResponsesServiceTier(body []byte, model *store.Model) []byte {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return body
	}
	if !applyConfiguredResponsesServiceTierRoot(root, model) {
		return body
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return encoded
}

func applyConfiguredResponsesServiceTierRoot(root map[string]json.RawMessage, model *store.Model) bool {
	if root == nil || model == nil || !strings.EqualFold(strings.TrimSpace(model.Provider), "openai") || strings.ToLower(strings.TrimSpace(model.ServiceTier)) != "fast" {
		return false
	}
	if raw, ok := root["service_tier"]; ok {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
			var tier string
			if json.Unmarshal(trimmed, &tier) != nil || !strings.EqualFold(strings.TrimSpace(tier), "default") {
				return false
			}
		}
	}
	root["service_tier"] = json.RawMessage(`"priority"`)
	return true
}

func responsesWithSystemRole(body []byte) ([]byte, bool, error) {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false, err
	}
	inputJSON, ok := parsed["input"]
	if !ok {
		return body, false, nil
	}
	var input []json.RawMessage
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		return body, false, nil
	}
	changed := false
	for index, rawItem := range input {
		var metadata struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		if json.Unmarshal(rawItem, &metadata) != nil || metadata.Type != "message" || metadata.Role != "developer" {
			continue
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return nil, false, err
		}
		item["role"] = json.RawMessage(`"system"`)
		updatedItem, err := json.Marshal(item)
		if err != nil {
			return nil, false, err
		}
		input[index] = updatedItem
		changed = true
	}
	if !changed {
		return body, false, nil
	}
	updatedInput, err := json.Marshal(input)
	if err != nil {
		return nil, false, err
	}
	parsed["input"] = updatedInput
	result, err := json.Marshal(parsed)
	if err != nil {
		return nil, false, err
	}
	return result, true, nil
}
