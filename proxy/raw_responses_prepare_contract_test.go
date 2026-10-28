package proxy

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-json"
	"kiro-proxy/converter"
	"kiro-proxy/store"
)

type rawResponsesPrepareContractFixture struct {
	name                 string
	endpoint             string
	body                 string
	modelReasoningEffort string
	autoReasoningEffort  *string
	requiredOutputFields []string
	cacheStabilityGroup  string
}

type rawResponsesPrepareContractGolden struct {
	UpstreamBody string `json:"upstream_body,omitempty"`
	Error        string `json:"error,omitempty"`
}

func rawResponsesPrepareContractFixtures() []rawResponsesPrepareContractFixture {
	fixed := func(value string) *string { return &value }
	return []rawResponsesPrepareContractFixture{
		{
			name:                 "responses_mixed_metadata_context_and_reasoning",
			endpoint:             "/responses",
			modelReasoningEffort: "low",
			requiredOutputFields: []string{"instructions", "input"},
			body:                 ` { "model" : "client-alias", "stream":true, "instructions":"stable policy", "origin":"kiro", "profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/synthetic", "input":[{"role":"user","content":"synthetic request"}], "reasoning":{"type":"enabled","effort":"medium","summary":"detailed"}, "metadata":{"kiro:conversation_id":"synthetic-conversation","kiro:agent_mode":"vibe","kiro:proxy_continuation_id":"private","kiro:proxy_current_tool_results":2,"custom":"keep"}, "extension":{"precise":900719925474099312345,"decimal":0.1000000000000000000001,"unknown":{"keep":true}} } `,
		},
		{
			name:                 "chat_mixed_metadata_context_and_reasoning",
			endpoint:             "/chat/completions",
			modelReasoningEffort: "low",
			requiredOutputFields: []string{"messages", "prompt_cache_key"},
			body:                 `{"model":"client-alias","stream":true,"instructions":"stable policy","origin":"kiro","profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/synthetic","input":[{"role":"user","content":"synthetic request"}],"reasoning":{"type":"enabled","effort":"medium","summary":"detailed"},"metadata":{"kiro:conversation_id":"synthetic-conversation","kiro:agent_mode":"vibe","kiro:proxy_continuation_id":"private","kiro:proxy_current_tool_results":2,"custom":"keep"},"extension":{"precise":900719925474099312345,"decimal":0.1000000000000000000001,"unknown":{"keep":true}}}`,
		},
		{
			name:                 "responses_empty_metadata_and_explicit_cache_key",
			endpoint:             "/responses",
			modelReasoningEffort: "medium",
			requiredOutputFields: []string{"input", "prompt_cache_key"},
			body:                 `{"model":"client-alias","input":"synthetic input","metadata":{},"prompt_cache_key":"client-cache-key"}`,
		},
		{
			name:                 "responses_no_metadata_and_default_effort",
			endpoint:             "/responses",
			modelReasoningEffort: "high",
			requiredOutputFields: []string{"input"},
			body:                 `{"model":"client-alias","input":"synthetic input","reasoning":{"type":"enabled"}}`,
		},
		{
			name:                 "responses_explicit_reasoning_effort_is_preserved",
			endpoint:             "/responses",
			modelReasoningEffort: "low",
			requiredOutputFields: []string{"input"},
			body:                 `{"model":"client-alias","input":"synthetic input","reasoning":{"type":"disabled","effort":"xhigh","summary":"concise"}}`,
		},
		{
			name:                 "responses_fixed_reasoning_effort_overrides_client",
			endpoint:             "/responses",
			modelReasoningEffort: "low",
			autoReasoningEffort:  fixed("medium"),
			requiredOutputFields: []string{"input"},
			body:                 `{"model":"client-alias","input":"synthetic input","reasoning":{"type":"enabled","effort":"high","summary":"keep"}}`,
		},
		{
			name:                 "responses_fixed_empty_reasoning_effort_clears_value",
			endpoint:             "/responses",
			modelReasoningEffort: "high",
			autoReasoningEffort:  fixed(""),
			requiredOutputFields: []string{"input"},
			body:                 `{"model":"client-alias","input":"synthetic input","reasoning":{"type":"enabled","effort":"high","summary":"keep"}}`,
		},
		{
			name:                 "responses_workflow_tool_schema_order_duplicates_and_precision",
			endpoint:             "/responses",
			modelReasoningEffort: "low",
			requiredOutputFields: []string{"input", "tools"},
			body:                 `{"model":"client-alias","input":"synthetic input","tools":` + rawWorkflowCompatTools + `}`,
		},
		{
			name:                 "chat_nested_tools_and_unknown_fields",
			endpoint:             "/chat/completions",
			modelReasoningEffort: "low",
			requiredOutputFields: []string{"messages", "tools"},
			body:                 `{"model":"client-alias","input":"synthetic input","tools":[{"type":"function","x-outer":9007199254740993,"function":{"name":"control_pwsh_process","strict":false,"x-inner":9007199254740993,"parameters":{"type":"object","properties":{"command":{"anyOf":[{"not":{}},{"type":"string"}]}}} }},{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`,
		},
		{
			name:                 "chat_auto_cache_key_static_prefix_turn_one",
			endpoint:             "/chat/completions",
			modelReasoningEffort: "low",
			requiredOutputFields: []string{"messages", "prompt_cache_key"},
			cacheStabilityGroup:  "chat_auto_cache_key_static_prefix",
			body:                 `{"model":"client-alias","instructions":"stable synthetic policy","input":[{"role":"user","content":"first synthetic turn"}]}`,
		},
		{
			name:                 "chat_auto_cache_key_static_prefix_turn_two",
			endpoint:             "/chat/completions",
			modelReasoningEffort: "low",
			requiredOutputFields: []string{"messages", "prompt_cache_key"},
			cacheStabilityGroup:  "chat_auto_cache_key_static_prefix",
			body:                 `{"model":"client-alias","instructions":"stable synthetic policy","input":[{"role":"user","content":"second, different synthetic turn"}]}`,
		},
		{
			name:     "responses_previous_response_id_rejected",
			endpoint: "/responses",
			body:     `{"model":"client-alias","input":"synthetic","previous_response_id":"synthetic-previous"}`,
		},
		{
			name:     "responses_conversation_rejected",
			endpoint: "/responses",
			body:     `{"model":"client-alias","input":"synthetic","conversation":{"id":"synthetic-conversation"}}`,
		},
		{
			name:     "responses_trailing_json_rejected",
			endpoint: "/responses",
			body:     `{"model":"client-alias","input":"synthetic"} {"extra":true}`,
		},
		{
			name:     "responses_invalid_metadata_rejected",
			endpoint: "/responses",
			body:     `{"model":"client-alias","input":"synthetic","metadata":["not","an","object"]}`,
		},
		{
			name:     "responses_invalid_reasoning_rejected",
			endpoint: "/responses",
			body:     `{"model":"client-alias","input":"synthetic","reasoning":[]}`,
		},
		{
			name:     "chat_invalid_reasoning_effort_rejected",
			endpoint: "/chat/completions",
			body:     `{"model":"client-alias","input":"synthetic","reasoning":{"effort":7}}`,
		},
		{
			name:     "chat_unsupported_tool_choice_rejected",
			endpoint: "/chat/completions",
			body:     `{"model":"client-alias","input":"synthetic","tool_choice":"strict"}`,
		},
		{
			name:     "responses_null_root_rejected",
			endpoint: "/responses",
			body:     `null`,
		},
		{
			name:                 "chat_function_call_output_object_extreme_explicit_cache",
			endpoint:             "/chat/completions",
			modelReasoningEffort: "low",
			body:                 `{ "model":"client-alias", "instructions":"stable synthetic policy", "input":[ { "type":"function_call_output", "call_id":"call-synthetic", "output":{ "huge":1e999, "s":"<>&\u2028" } } ], "prompt_cache_key":"explicit-synthetic-cache" }`,
		},
		{
			name:                 "chat_message_content_object_extreme_auto_cache",
			endpoint:             "/chat/completions",
			modelReasoningEffort: "low",
			body:                 `{ "model":"client-alias", "instructions":"stable synthetic policy", "input":[ { "type":"message", "role":"assistant", "content":{ "huge":1e999, "s":"<>&\u2028" } } ] }`,
		},
		{
			name:                 "responses_extreme_number_and_escaped_text",
			endpoint:             "/responses",
			modelReasoningEffort: "low",
			body:                 `{ "model":"client-alias", "input":"synthetic", "prompt_cache_key":"explicit-responses-cache", "extension":{ "huge":1e999, "small":0.0000000000000000000000000001, "text":"<>&\u2028", "nested":{ "number":900719925474099312345 } } }`,
		},
		{
			name:                 "responses_finite_extreme_number_and_escaped_text",
			endpoint:             "/responses",
			modelReasoningEffort: "low",
			body:                 `{ "model":"client-alias", "input":"synthetic", "prompt_cache_key":"explicit-responses-finite-cache", "extension":{ "huge":1e308, "small":0.1000000000000000000001, "text":"<>&\u2028", "nested":{ "number":900719925474099312345 } } }`,
		},
		{
			name:                 "chat_unknown_extension_extreme_explicit_cache",
			endpoint:             "/chat/completions",
			modelReasoningEffort: "low",
			body:                 `{ "model":"client-alias", "instructions":"synthetic", "input":"synthetic", "prompt_cache_key":"explicit-unknown-cache", "unknown_extension":{ "huge":1e999 } }`,
		},
		{
			name:                 "chat_invalid_instructions_with_unknown_extension_extreme",
			endpoint:             "/chat/completions",
			modelReasoningEffort: "low",
			body:                 `{ "model":"client-alias", "instructions":{  "bad"  : true }, "input":"synthetic", "unknown_extension":{ "huge":1e999 } }`,
		},
		{
			name:                 "responses_origin_rewrite_reasoning_extreme_fixed_effort",
			endpoint:             "/responses",
			modelReasoningEffort: "low",
			autoReasoningEffort:  fixed("high"),
			body:                 `{ "model":"client-alias", "origin":"kiro", "instructions":"synthetic", "input":"synthetic", "reasoning":{ "effort":1e999 } }`,
		},
		{
			name:                 "responses_no_origin_reasoning_extreme_fixed_effort",
			endpoint:             "/responses",
			modelReasoningEffort: "low",
			autoReasoningEffort:  fixed("high"),
			body:                 `{ "model":"client-alias", "instructions":"synthetic", "input":"synthetic", "reasoning":{ "effort":1e999 } }`,
		},
		{
			name:                 "chat_invalid_instructions_object_whitespace",
			endpoint:             "/chat/completions",
			modelReasoningEffort: "low",
			body:                 `{ "model":"client-alias", "instructions":{  "bad"  : true }, "input":"synthetic" }`,
		},
		{
			name:                 "chat_invalid_input_object_whitespace",
			endpoint:             "/chat/completions",
			modelReasoningEffort: "low",
			body:                 `{ "model":"client-alias", "instructions":"synthetic", "input":{  "bad"  : true } }`,
		},
	}
}

func TestPrepareRawResponsesUpstreamBodyMatchesFrozenContract(t *testing.T) {
	baselinePath := filepath.Join("testdata", "raw_responses_prepare_baseline.json")
	baselineBytes, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("read frozen raw Responses prepare baseline: %v", err)
	}
	var baseline map[string]rawResponsesPrepareContractGolden
	if err := json.Unmarshal(baselineBytes, &baseline); err != nil {
		t.Fatalf("decode frozen raw Responses prepare baseline: %v", err)
	}
	fixtures := rawResponsesPrepareContractFixtures()
	if len(baseline) != len(fixtures) {
		t.Fatalf("frozen baseline has %d cases; fixtures have %d", len(baseline), len(fixtures))
	}

	cacheKeys := make(map[string][]string)
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			want, ok := baseline[fixture.name]
			if !ok {
				t.Fatalf("frozen baseline is missing fixture %q", fixture.name)
			}
			model := &store.Model{
				ModelID: "synthetic-upstream-model", Provider: "openai",
				BaseURL: "https://api.example.invalid/v1", Endpoint: fixture.endpoint,
				ReasoningEffort: fixture.modelReasoningEffort,
			}
			var pre *Preflight
			if fixture.autoReasoningEffort != nil {
				effort := *fixture.autoReasoningEffort
				pre = &Preflight{SourceModel: "auto", AutoReasoningEffort: &effort, Conv: &converter.ParsedConversation{ModelID: "auto"}}
			}
			input := []byte(fixture.body)
			original := append([]byte(nil), input...)
			gotBody, gotErr := prepareRawResponsesUpstreamBody(input, pre, model)
			if !bytes.Equal(input, original) {
				t.Fatalf("prepare mutated its input: before=%q after=%q", original, input)
			}
			if gotErr != nil {
				if gotErr.Error() != want.Error {
					t.Fatalf("error changed:\n got: %q\nwant: %q", gotErr.Error(), want.Error)
				}
				if want.UpstreamBody != "" {
					t.Fatalf("frozen case has both an error and upstream body: %q", want.UpstreamBody)
				}
				return
			}
			if want.Error != "" {
				t.Fatalf("error changed: got no error, want %q", want.Error)
			}
			if !bytes.Equal(gotBody, []byte(want.UpstreamBody)) {
				t.Fatalf("upstream body changed:\n got: %s\nwant: %s", gotBody, want.UpstreamBody)
			}
			fields := rawResponsesPrepareExtractContractFields(t, gotBody, fixture.endpoint)
			for _, field := range fixture.requiredOutputFields {
				if _, ok := fields[field]; !ok {
					t.Errorf("prepared body is missing required %s field: %s", field, gotBody)
				}
			}
			if fixture.cacheStabilityGroup != "" {
				cacheKeys[fixture.cacheStabilityGroup] = append(cacheKeys[fixture.cacheStabilityGroup], string(fields["prompt_cache_key"]))
			}
		})
	}
	for group, keys := range cacheKeys {
		if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
			t.Errorf("prompt_cache_key is not stable across new input turns for %s: %q", group, keys)
		}
	}
}

func rawResponsesPrepareExtractContractFields(t *testing.T, body []byte, endpoint string) map[string]json.RawMessage {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("decode prepared raw Responses body for field extraction: %v", err)
	}
	fields := []string{"prompt_cache_key"}
	if endpoint == "/responses" {
		fields = append(fields, "instructions", "tools", "input")
	} else {
		fields = append(fields, "messages", "tools")
	}
	selected := make(map[string]json.RawMessage)
	for _, field := range fields {
		if raw, ok := root[field]; ok {
			selected[field] = append(json.RawMessage(nil), raw...)
		}
	}
	return selected
}
