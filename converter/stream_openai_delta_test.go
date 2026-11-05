package converter

import (
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func TestOpenAIDeltaDecodeShapes(t *testing.T) {
	tests := []struct {
		name          string
		delta         string
		wantContent   string
		wantReasoning string
		wantField     string
		wantSkipped   []string
		wantOpaque    []string
	}{
		{name: "string content", delta: `{"content":"hi"}`, wantContent: "hi"},
		{name: "null fields", delta: `{"content":null,"reasoning_content":null,"reasoning":null}`},
		{name: "empty string reasoning is not opaque", delta: `{"reasoning_content":"","reasoning_details":[]}`},
		// Mistral 分段 content，形态取自 magpie internal/gateway/mistral_parts_test.go（#483）
		{name: "mistral parts", delta: `{"content":[{"type":"thinking","thinking":[{"type":"text","text":" asked for ok."}],"closed":true},{"type":"text","text":"o"}]}`, wantContent: "o", wantSkipped: []string{"thinking"}},
		{name: "string and image parts", delta: `{"content":["a",{"type":"text","text":"b"},{"type":"image_url","image_url":{"url":"x"}}]}`, wantContent: "ab", wantSkipped: []string{"image_url"}},
		{name: "reasoning object", delta: `{"reasoning":{"text":"plan"}}`, wantReasoning: "plan", wantField: "reasoning"},
		{name: "reasoning object summary", delta: `{"reasoning":{"summary":[{"type":"summary_text","text":"brief"}]}}`, wantReasoning: "brief", wantField: "reasoning"},
		{name: "reasoning_details text and summary", delta: `{"reasoning_details":[{"type":"reasoning.text","text":"a","index":0},{"type":"reasoning.summary","summary":"b"}]}`, wantReasoning: "ab", wantField: "reasoning_details"},
		// 加密推理条目取自 magpie internal/gateway/chat_tool_reasoning_test.go
		{name: "encrypted reasoning_details", delta: `{"reasoning_details":[{"type":"reasoning.encrypted","data":"gAAAA"}]}`, wantOpaque: []string{"reasoning_details"}},
		{name: "first non-empty field wins", delta: `{"reasoning_content":"same","reasoning_details":[{"type":"reasoning.text","text":"same"}]}`, wantReasoning: "same", wantField: "reasoning_content"},
		{name: "falls through to reasoning_details", delta: `{"reasoning":"","reasoning_details":[{"type":"reasoning.text","text":"d"}]}`, wantReasoning: "d", wantField: "reasoning_details"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var delta openAIDelta
			if err := json.Unmarshal([]byte(tt.delta), &delta); err != nil {
				t.Fatalf("decode delta: %v", err)
			}
			got, err := delta.decodeText()
			if err != nil {
				t.Fatalf("decodeText: %v", err)
			}
			if got.Content != tt.wantContent || got.Reasoning != tt.wantReasoning || got.ReasoningField != tt.wantField {
				t.Fatalf("got content=%q reasoning=%q field=%q", got.Content, got.Reasoning, got.ReasoningField)
			}
			if strings.Join(got.SkippedParts, ",") != strings.Join(tt.wantSkipped, ",") || strings.Join(got.OpaqueFields, ",") != strings.Join(tt.wantOpaque, ",") {
				t.Fatalf("got skipped=%v opaque=%v, want %v/%v", got.SkippedParts, got.OpaqueFields, tt.wantSkipped, tt.wantOpaque)
			}
		})
	}
}

func TestOpenAIDeltaDecodeRejectsUnknownShapes(t *testing.T) {
	for _, delta := range []string{`{"content":42}`, `{"content":true}`, `{"content":{"text":"x"}}`, `{"reasoning":7}`, `{"reasoning_details":false}`} {
		var d openAIDelta
		if err := json.Unmarshal([]byte(delta), &d); err != nil {
			t.Fatalf("decode %s: %v", delta, err)
		}
		if _, err := d.decodeText(); err == nil {
			t.Errorf("%s should be rejected", delta)
		}
	}
}
