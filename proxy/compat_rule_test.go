package proxy

import (
	"bytes"
	"testing"

	json "github.com/goccy/go-json"
)

type compatRuleCase struct {
	name      string
	rule      compatRule
	protocol  compatProtocol
	modelID   string // 空时使用非 Claude 的默认测试模型
	status    int
	errBody   string
	body      string
	wantValue string // 空表示不应识别
	wantBody  string
}

func runCompatRuleCases(t *testing.T, cases []compatRuleCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.rule.appliesTo(tc.protocol) {
				t.Fatalf("rule %s does not apply to %s", tc.rule.kind, tc.protocol)
			}
			target := compatTarget{protocol: tc.protocol, modelID: tc.modelID}
			if target.modelID == "" {
				target.modelID = "cap-upstream"
			}
			value, evidence, ok := tc.rule.recognize(target, newCompatRefusal(tc.status, []byte(tc.errBody)), []byte(tc.body))
			if tc.wantValue == "" {
				if ok {
					t.Fatalf("unexpected match value=%q evidence=%q", value, evidence)
				}
				return
			}
			if !ok || value != tc.wantValue || evidence == "" {
				t.Fatalf("recognize = %q, %q, %v; want %q", value, evidence, ok, tc.wantValue)
			}
			updated, changed, err := tc.rule.apply(target, []byte(tc.body), value)
			if err != nil || !changed {
				t.Fatalf("apply changed=%v err=%v", changed, err)
			}
			assertCompatJSONEqual(t, updated, tc.wantBody)
			if _, again, err := tc.rule.apply(target, updated, value); err != nil || again {
				t.Fatalf("apply is not idempotent: changed=%v err=%v", again, err)
			}
		})
	}
}

func assertCompatJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue interface{}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("body = %s, want %s", gotJSON, wantJSON)
	}
}

func TestCompatRoleAndTokenRules(t *testing.T) {
	runCompatRuleCases(t, []compatRuleCase{{
		// magpie codex_backend.go developerItemsAsSystem 记录的 Codex compaction 400。
		name: "responses developer refused", rule: developerRoleRule, protocol: compatProtocolResponses, status: 400,
		errBody:   `{"error":{"message":"developer is not one of ['system', 'assistant', 'user', 'tool', 'function']"}}`,
		body:      `{"input":[{"type":"message","role":"developer","content":"rules"},{"role":"developer","content":"x"},{"type":"message","role":"user","content":"hi"}]}`,
		wantValue: "system",
		wantBody:  `{"input":[{"type":"message","role":"system","content":"rules"},{"role":"system","content":"x"},{"type":"message","role":"user","content":"hi"}]}`,
	}, {
		// 项目既有判定 isResponsesDeveloperRoleRejected 覆盖的措辞。
		name: "chat role list without developer", rule: developerRoleRule, protocol: compatProtocolChat, status: 400,
		errBody:   `{"error":{"type":"invalid_request_error","message":"message role must be one of system, user, assistant, tool"}}`,
		body:      `{"messages":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}]}`,
		wantValue: "system",
		wantBody:  `{"messages":[{"role":"system","content":"rules"},{"role":"user","content":"hi"}]}`,
	}, {
		name: "developer listed as allowed", rule: developerRoleRule, protocol: compatProtocolChat, status: 400,
		errBody: `{"error":{"type":"invalid_request_error","message":"message role must be one of system, developer, user"}}`,
		body:    `{"messages":[{"role":"developer","content":"rules"}]}`,
	}, {
		name: "no developer message sent", rule: developerRoleRule, protocol: compatProtocolResponses, status: 400,
		errBody: `{"error":{"message":"developer is not one of ['system', 'user']"}}`,
		body:    `{"input":[{"type":"message","role":"user","content":"hi"}]}`,
	}, {
		// magpie provider/token_field.go 记录的 OpenAI 新模型报错。
		name: "use max_completion_tokens", rule: tokenFieldRule, protocol: compatProtocolChat, status: 400,
		errBody:   `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","param":"max_tokens","code":"unsupported_parameter"}}`,
		body:      `{"model":"m","max_tokens":512,"messages":[]}`,
		wantValue: "max_completion_tokens",
		wantBody:  `{"model":"m","max_completion_tokens":512,"messages":[]}`,
	}, {
		name: "relay asks for max_tokens", rule: tokenFieldRule, protocol: compatProtocolChat, status: 400,
		errBody:   `{"error":{"message":"max_completion_tokens is not supported, use max_tokens instead"}}`,
		body:      `{"max_completion_tokens":256,"messages":[]}`,
		wantValue: "max_tokens",
		wantBody:  `{"max_tokens":256,"messages":[]}`,
	}, {
		name: "limit too high is not a field rename", rule: tokenFieldRule, protocol: compatProtocolChat, status: 400,
		errBody: `{"error":{"message":"max_tokens must be at most 32768, use a smaller value"}}`,
		body:    `{"max_tokens":200000,"messages":[]}`,
	}, {
		name: "asked field already sent", rule: tokenFieldRule, protocol: compatProtocolChat, status: 400,
		errBody: `{"error":{"message":"Use 'max_completion_tokens' instead."}}`,
		body:    `{"max_completion_tokens":512,"messages":[]}`,
	}})
	if developerRoleRule.appliesTo(compatProtocolCodex) || tokenFieldRule.appliesTo(compatProtocolResponses) {
		t.Fatal("role/token rules must stay scoped to their protocols")
	}
}

func TestCompatSamplingRule(t *testing.T) {
	runCompatRuleCases(t, []compatRuleCase{{
		// magpie anthropic.go samplingDeprecated 记录的 Haiku 5.5 报错。
		name: "anthropic temperature deprecated", rule: samplingRule, protocol: compatProtocolAnthropic, status: 400,
		errBody:   `{"type":"error","error":{"type":"invalid_request_error","message":"` + "`temperature`" + ` is deprecated for this model."}}`,
		body:      `{"model":"claude","max_tokens":10,"temperature":0,"top_p":0.9,"messages":[]}`,
		wantValue: compatSamplingOmitted,
		wantBody:  `{"model":"claude","max_tokens":10,"messages":[]}`,
	}, {
		// OpenAI 推理模型公开报错措辞（需实测）。
		name: "openai reasoning model temperature", rule: samplingRule, protocol: compatProtocolChat, status: 400,
		errBody:   `{"error":{"message":"Unsupported value: 'temperature' does not support 0.7 with this model. Only the default (1) value is supported.","param":"temperature","code":"unsupported_value"}}`,
		body:      `{"temperature":0.7,"messages":[]}`,
		wantValue: compatSamplingOmitted,
		wantBody:  `{"messages":[]}`,
	}, {
		name: "validator extra input", rule: samplingRule, protocol: compatProtocolResponses, status: 422,
		errBody:   `{"detail":[{"loc":["body","top_p"],"msg":"Extra inputs are not permitted","type":"extra_forbidden"}]}`,
		body:      `{"top_p":0.5,"input":[]}`,
		wantValue: compatSamplingOmitted,
		wantBody:  `{"input":[]}`,
	}, {
		name: "range error is not a refusal", rule: samplingRule, protocol: compatProtocolChat, status: 400,
		errBody: `{"error":{"message":"temperature must be between 0 and 2"}}`,
		body:    `{"temperature":5,"messages":[]}`,
	}, {
		name: "no sampling sent", rule: samplingRule, protocol: compatProtocolChat, status: 400,
		errBody: `{"error":{"message":"temperature is not supported with this model"}}`,
		body:    `{"messages":[]}`,
	}})
	if samplingRule.appliesTo(compatProtocolGemini) {
		t.Fatal("sampling rule must not rewrite Gemini generationConfig")
	}
}

func TestCompatThinkingRules(t *testing.T) {
	const enabledBody = `{"thinking":{"type":"enabled","budget_tokens":1024},"max_tokens":2048,"messages":[]}`
	chineseRefusal, _ := json.Marshal(map[string]interface{}{"error": map[string]string{"message": "该模型始终支持思考，不可关闭"}})
	runCompatRuleCases(t, []compatRuleCase{{
		// magpie anthropic.go alwaysThinks 记录的 Z.ai GLM 报错。
		name: "always engages in thinking", rule: thinkingEnabledRule, protocol: compatProtocolAnthropic, status: 400,
		errBody:   `{"error":{"code":"1210","message":"The model always engages in thinking and the thinking cannot be disabled"}}`,
		body:      `{"thinking":{"type":"disabled"},"max_tokens":512,"temperature":0.2,"messages":[]}`,
		wantValue: compatThinkingEnabledValue,
		wantBody:  `{"thinking":{"type":"enabled","budget_tokens":1024},"max_tokens":1536,"messages":[]}`,
	}, {
		// 同上，JSON 中以 \uXXXX 转义的中文（magpie #1678）。
		name: "escaped chinese refusal", rule: thinkingEnabledRule, protocol: compatProtocolAnthropic, status: 400,
		errBody:   string(chineseRefusal),
		body:      `{"thinking":{"type":"disabled"},"max_tokens":2048,"messages":[]}`,
		wantValue: compatThinkingEnabledValue,
		wantBody:  enabledBody,
	}, {
		// magpie anthropic.go thinkingOffRefused 记录的 Opus 报错。
		name: "opus disabled not supported", rule: thinkingEnabledRule, protocol: compatProtocolAnthropic, status: 400,
		errBody:   `{"error":{"message":"\"thinking.type.disabled\" is not supported for this model. Use \"thinking.type.adaptive\" instead."}}`,
		body:      `{"thinking":{"type":"disabled"},"max_tokens":2048,"messages":[]}`,
		wantValue: compatThinkingEnabledValue,
		wantBody:  enabledBody,
	}, {
		// magpie thinking_needed.go 记录的天翼云中转：未发送 thinking。
		name: "thinking type must be enabled", rule: thinkingEnabledRule, protocol: compatProtocolAnthropic, status: 400,
		errBody:   `{"error":{"message":"thinking.type must be \"enabled\""}}`,
		body:      `{"max_tokens":2048,"messages":[]}`,
		wantValue: compatThinkingEnabledValue,
		wantBody:  enabledBody,
	}, {
		name: "adaptive is left as sent", rule: thinkingEnabledRule, protocol: compatProtocolAnthropic, status: 400,
		errBody: `{"error":{"message":"thinking.type must be \"enabled\""}}`,
		body:    `{"thinking":{"type":"adaptive"},"max_tokens":2048,"messages":[]}`,
	}, {
		name: "temperature constraint is not always-thinks", rule: thinkingEnabledRule, protocol: compatProtocolAnthropic, status: 400,
		errBody: `{"error":{"message":"temperature may only be set to 1 when thinking is enabled"}}`,
		body:    `{"thinking":{"type":"disabled"},"max_tokens":2048,"messages":[]}`,
	}, {
		// 同一 must be "enabled" 报错针对 adaptive：按原档位改为 converter 构造的 budget（high → 8192）。
		name: "adaptive refused becomes budget", rule: thinkingAdaptiveRule, protocol: compatProtocolAnthropic, modelID: "claude-opus-4-7", status: 400,
		errBody:   `{"error":{"message":"thinking.type must be \"enabled\""}}`,
		body:      `{"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"high"},"max_tokens":16000,"temperature":1,"messages":[]}`,
		wantValue: compatThinkingAdaptiveValue,
		wantBody:  `{"thinking":{"type":"enabled","budget_tokens":8192},"max_tokens":16000,"messages":[]}`,
	}, {
		// converter 对 GLM-5.3 在 budget 之外仍下发 output_config.effort。
		name: "adaptive refused keeps converter output effort", rule: thinkingAdaptiveRule, protocol: compatProtocolAnthropic, modelID: "glm-5.3", status: 400,
		errBody:   "{\"error\":{\"message\":\"thinking.type: unknown variant `adaptive`, expected one of `enabled`, `disabled`\"}}",
		body:      `{"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},"max_tokens":8000,"messages":[]}`,
		wantValue: compatThinkingAdaptiveValue,
		wantBody:  `{"thinking":{"type":"enabled","budget_tokens":4096},"output_config":{"effort":"medium"},"max_tokens":8000,"messages":[]}`,
	}, {
		name: "adaptive budget does not fit max_tokens", rule: thinkingAdaptiveRule, protocol: compatProtocolAnthropic, status: 400,
		errBody: `{"error":{"message":"thinking.type must be \"enabled\""}}`,
		body:    `{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"max_tokens":1500,"messages":[]}`,
	}, {
		// magpie anthropic_shapes.go 记录的 Vertex 中转报错。
		name: "display value refused", rule: thinkingDisplayRule, protocol: compatProtocolAnthropic, status: 400,
		errBody:   `{"error":{"message":"thinking.adaptive.display: Input should be 'summarized', 'omitted'"}}`,
		body:      `{"thinking":{"type":"adaptive","display":"updates"},"messages":[]}`,
		wantValue: compatThinkingDisplayValue,
		wantBody:  `{"thinking":{"type":"adaptive"},"messages":[]}`,
	}, {
		name: "display not sent", rule: thinkingDisplayRule, protocol: compatProtocolAnthropic, status: 400,
		errBody: `{"error":{"message":"thinking.adaptive.display: Extra inputs are not permitted"}}`,
		body:    `{"thinking":{"type":"adaptive"},"messages":[]}`,
	}})
	if thinkingEnabledRule.appliesTo(compatProtocolChat) {
		t.Fatal("Anthropic thinking rule must not apply to Chat")
	}
}
