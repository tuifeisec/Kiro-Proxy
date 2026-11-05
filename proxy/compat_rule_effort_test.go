package proxy

import "testing"

func TestCompatReasoningEffortRule(t *testing.T) {
	runCompatRuleCases(t, []compatRuleCase{{
		// magpie gateway.go otherOffNamed 记录的 OpenAI 报错措辞；被拒档位换成本地可表达的 xhigh。
		name: "openai level refused with list", rule: reasoningEffortRule, protocol: compatProtocolResponses, status: 400,
		errBody:   `{"error":{"message":"Unsupported value: 'xhigh' is not supported with the 'gpt-5.1' model. Supported values are: 'none', 'minimal', 'low', 'medium', and 'high'.","param":"reasoning.effort","code":"unsupported_value"}}`,
		body:      `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[]}`,
		wantValue: "none|low|medium|high",
		wantBody:  `{"reasoning":{"effort":"high","summary":"auto"},"input":[]}`,
	}, {
		// magpie anthropic.go refusesThinkingOff 记录的 Sub2API 报错（#1465）。
		name: "sub2api none refused", rule: reasoningEffortRule, protocol: compatProtocolChat, status: 400,
		errBody:   `{"error":{"message":"gpt-6.1-sol does not support reasoning effort \"none\"; use low, medium, high, xhigh or max"}}`,
		body:      `{"reasoning_effort":"none","messages":[]}`,
		wantValue: "low|medium|high|xhigh|max",
		wantBody:  `{"reasoning_effort":"low","messages":[]}`,
	}, {
		// magpie gateway.go effortLevelsNamed 记录的 Command Code 报错。
		name: "command code expected one of", rule: reasoningEffortRule, protocol: compatProtocolChat, status: 400,
		errBody:   `{"error":{"message":"Invalid reasoning_effort: expected one of \"low\"|\"medium\"|\"high\"|\"xhigh\"|\"max\""}}`,
		body:      `{"reasoning_effort":"none","messages":[]}`,
		wantValue: "low|medium|high|xhigh|max",
		wantBody:  `{"reasoning_effort":"low","messages":[]}`,
	}, {
		name: "anthropic output_config effort nearest", rule: reasoningEffortRule, protocol: compatProtocolAnthropic, status: 400,
		errBody:   `{"error":{"message":"output_config.effort: Input should be 'low', 'medium' or 'high'"}}`,
		body:      `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"messages":[]}`,
		wantValue: "low|medium|high",
		wantBody:  `{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[]}`,
	}, {
		name: "another param's levels", rule: reasoningEffortRule, protocol: compatProtocolResponses, status: 400,
		errBody: `{"error":{"message":"Unsupported value: 'high' is not supported. Supported values are: 'low' and 'medium'.","param":"text.verbosity"}}`,
		body:    `{"reasoning":{"effort":"high"},"text":{"verbosity":"high"},"input":[]}`,
	}, {
		name: "sent level is among listed", rule: reasoningEffortRule, protocol: compatProtocolChat, status: 400,
		errBody: `{"error":{"message":"reasoning_effort not supported with tools; supported values are: none, low"}}`,
		body:    `{"reasoning_effort":"low","messages":[]}`,
	}, {
		name: "no level list", rule: reasoningEffortRule, protocol: compatProtocolChat, status: 400,
		errBody: `{"error":{"message":"reasoning_effort is not supported with this model"}}`,
		body:    `{"reasoning_effort":"high","messages":[]}`,
	}, {
		// magpie automode.go effortFieldUnknown 记录的 OpenAI 报错。
		name: "openai unrecognized reasoning_effort", rule: reasoningFieldRule, protocol: compatProtocolChat, status: 400,
		errBody:   `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort"}}`,
		body:      `{"reasoning_effort":"high","messages":[]}`,
		wantValue: compatReasoningOmitted,
		wantBody:  `{"messages":[]}`,
	}, {
		name: "validator extra reasoning", rule: reasoningFieldRule, protocol: compatProtocolResponses, status: 422,
		errBody:   `{"detail":[{"type":"extra_forbidden","loc":["body","reasoning"],"msg":"Extra inputs are not permitted"}]}`,
		body:      `{"reasoning":{"effort":"high","summary":"auto"},"input":[]}`,
		wantValue: compatReasoningOmitted,
		wantBody:  `{"input":[]}`,
	}, {
		name: "anthropic output_config effort unknown", rule: reasoningFieldRule, protocol: compatProtocolAnthropic, status: 400,
		errBody:   `{"error":{"message":"output_config: Extra inputs are not permitted"}}`,
		body:      `{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[]}`,
		wantValue: compatReasoningOmitted,
		wantBody:  `{"thinking":{"type":"adaptive"},"messages":[]}`,
	}, {
		// magpie anthropic_shapes.go 记录的逐条消息 output_config 被拒，不是顶层字段问题。
		name: "per message output_config", rule: reasoningFieldRule, protocol: compatProtocolAnthropic, status: 400,
		errBody: `{"error":{"message":"messages.1.output_config: Extra inputs are not permitted"}}`,
		body:    `{"output_config":{"effort":"high"},"messages":[]}`,
	}, {
		name: "value refusal is not a field refusal", rule: reasoningFieldRule, protocol: compatProtocolChat, status: 400,
		errBody: `{"error":{"message":"reasoning_effort 'high' is not supported with this model"}}`,
		body:    `{"reasoning_effort":"high","messages":[]}`,
	}, {
		name: "field not sent", rule: reasoningFieldRule, protocol: compatProtocolChat, status: 400,
		errBody: `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort"}}`,
		body:    `{"messages":[]}`,
	}})
}

// 就近映射由 converter.FitReasoningEffort 统一实现；这里只验证兼容层的取舍：无需或无法映射时不改写。
func TestCompatFitEffort(t *testing.T) {
	cases := []struct {
		sent    string
		allowed []string
		want    string
	}{
		{"none", []string{"low", "medium", "high"}, "low"},
		{"xhigh", []string{"low", "medium", "high"}, "high"},
		{"medium", []string{"low", "medium"}, ""},
		{"minimal", []string{"low"}, ""},
		{"high", []string{"none"}, ""},
	}
	for _, tc := range cases {
		if got := compatFitEffort(tc.sent, tc.allowed); got != tc.want {
			t.Fatalf("fit(%q, %v) = %q, want %q", tc.sent, tc.allowed, got, tc.want)
		}
	}
}

func TestCompatContinuationRefusal(t *testing.T) {
	sent := []byte(`{"previous_response_id":"resp_1","input":[]}`)
	positive := `{"error":{"message":"Unsupported parameter: 'previous_response_id'","param":"previous_response_id","code":"unsupported_parameter"}}`
	session := newCompatSession(newModelCapabilityStore(), capabilityTestModel(), compatProtocolResponses, "", nil, nil)
	session.noteContinuationRefusal(400, []byte(positive), sent)
	if len(session.pending) != 1 || session.pending[0].kind != capabilityContinuation {
		t.Fatalf("unsupported previous_response_id was not noted: %+v", session.pending)
	}
	if _, ok := session.caps.lookup(session.model, compatProtocolResponses, capabilityContinuation, timeNowForCompatTest()); ok {
		t.Fatal("continuation refusal must not be learned before the full request succeeds")
	}
	session.finish(compatTestResponse(200, `{}`))
	if next := newCompatSession(session.caps, session.model, compatProtocolResponses, "", nil, nil); !next.continuationDisabled() {
		t.Fatal("successful full fallback should disable continuation within TTL")
	}
	for name, errBody := range map[string]string{
		"expired session": `{"error":{"message":"Previous response with id 'resp_1' not found.","param":"previous_response_id"}}`,
		"invalid id":      `{"error":{"message":"Invalid 'previous_response_id': expected an ID that begins with 'resp'."}}`,
	} {
		other := newCompatSession(newModelCapabilityStore(), capabilityTestModel(), compatProtocolResponses, "", nil, nil)
		other.noteContinuationRefusal(400, []byte(errBody), sent)
		if len(other.pending) != 0 {
			t.Fatalf("%s must not be learned as unsupported continuation", name)
		}
	}
}

func TestIsFormatIncompatible(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"unknown parameter", 400, `{"error":{"message":"Unknown parameter: 'prompt_cache_options'.","code":"unknown_parameter"}}`, true},
		{"validator extra input", 422, `{"detail":[{"loc":["body","service_tier"],"msg":"Extra inputs are not permitted","type":"extra_forbidden"}]}`, true},
		{"gemini unknown name", 400, `{"error":{"code":400,"message":"Invalid JSON payload received. Unknown name \"thinkingLevel\" at 'generation_config.thinking_config': Cannot find field.","status":"INVALID_ARGUMENT"}}`, true},
		{"invalid value on effort", 400, `{"error":{"message":"Invalid value for reasoning_effort: 'max'"}}`, true},
		{"not supported by this model with field", 400, `{"error":{"message":"\"thinking.type.disabled\" is not supported for this model."}}`, true},
		{"context length", 400, `{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens.","code":"context_length_exceeded"}}`, false},
		{"context length despite unknown field words", 400, `{"error":{"message":"Unsupported parameter: input exceeds the context window"}}`, false},
		{"content safety", 400, `{"error":{"message":"The response was filtered due to the prompt triggering content management policy.","code":"content_filter"}}`, false},
		{"auth", 400, `{"error":{"message":"Incorrect API key provided: sk-****. Unknown parameter rejected.","code":"invalid_api_key"}}`, false},
		{"quota", 400, `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","code":"insufficient_quota"}}`, false},
		{"model missing", 400, `{"error":{"message":"The model 'gpt-x' does not exist or you do not have access to it.","code":"model_not_found"}}`, false},
		{"invalid value without field word", 400, `{"error":{"message":"Invalid value: 'foo'"}}`, false},
		{"image capability handled elsewhere", 400, `{"error":{"message":"This model does not support image input"}}`, false},
		{"plain bad request", 400, `{"error":{"message":"Bad request"}}`, false},
		{"server error status", 500, `{"error":{"message":"Unknown parameter: 'x'"}}`, false},
		// 流式解析失败在 200 之后以 UpstreamProtocolFailure 终止，不是格式类 400；样本含任意上游文本也不得误判。
		{"stream decode failure after 200", 200, `无法解析上游 Chat 流事件: invalid character; 样本(40 字节): "{\"error\":\"Unknown parameter: 'x'\"}"`, false},
		{"stream decode failure text even as 400", 400, `无法解析上游 Responses 流事件: unexpected end; 样本(30 字节): "Unsupported parameter: tools"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evidence, got := isFormatIncompatible(tc.status, []byte(tc.body))
			if got != tc.want || got && evidence == "" {
				t.Fatalf("isFormatIncompatible = %v (%q), want %v", got, evidence, tc.want)
			}
		})
	}
}
