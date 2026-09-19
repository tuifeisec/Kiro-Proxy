package converter

import "testing"

func TestParseKiroRequestReasoningEffortOverride(t *testing.T) {
	tests := []struct {
		name   string
		fields string
		want   string
	}{
		{
			name:   "nested reasoning effort",
			fields: `"reasoning":{"effort":"HIGH"}`,
			want:   "high",
		},
		{
			name:   "legacy flat reasoning effort",
			fields: `"reasoningEffort":"LOW"`,
			want:   "low",
		},
		{
			name:   "nested effort takes precedence",
			fields: `"reasoningEffort":"extreme","reasoning":{"effort":"MEDIUM"}`,
			want:   "medium",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"conversationState":{"conversationId":"reasoning-test","currentMessage":{"userInputMessage":{"modelId":"gpt-5.6-sol","content":"hello"}}},"additionalModelRequestFields":{` + tt.fields + `}}`)
			conv, err := ParseKiroRequest(body)
			if err != nil {
				t.Fatalf("parse reasoning effort: %v", err)
			}
			if conv.ReasoningEffort != tt.want {
				t.Fatalf("reasoning effort = %q, want %q", conv.ReasoningEffort, tt.want)
			}
		})
	}
}

func TestParseKiroRequestRejectsUnsupportedNestedReasoningEffort(t *testing.T) {
	body := []byte(`{"conversationState":{"conversationId":"reasoning-test","currentMessage":{"userInputMessage":{"modelId":"gpt-5.6-sol","content":"hello"}}},"additionalModelRequestFields":{"reasoning":{"effort":"extreme"}}}`)

	if _, err := ParseKiroRequest(body); err == nil {
		t.Fatal("unsupported nested reasoning effort should be rejected")
	}
}
