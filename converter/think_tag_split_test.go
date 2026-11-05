package converter

import "testing"

// feedAll 依次喂入片段并在最后 finish，返回拼接后的推理、正文与是否未闭合。
func feedAll(s *thinkTagSplitter, chunks ...string) (reasoning, content string, unclosed bool) {
	collect := func(out thinkSplit) {
		reasoning += out.Reasoning
		content += out.Content
		unclosed = unclosed || out.Unclosed
	}
	for _, chunk := range chunks {
		collect(s.feed(chunk))
	}
	collect(s.finish())
	return reasoning, content, unclosed
}

func TestThinkTagSplitterSplitsLeadingBlock(t *testing.T) {
	tests := []struct {
		name          string
		chunks        []string
		wantReasoning string
		wantContent   string
	}{
		{name: "single chunk", chunks: []string{"<think>plan</think>answer"}, wantReasoning: "plan", wantContent: "answer"},
		{name: "leading whitespace and CRLF", chunks: []string{"\r\n <think>plan</think>\r\n\r\nanswer"}, wantReasoning: "plan", wantContent: "answer"},
		// 分片方式参照 magpie internal/gateway/minimax_think_test.go 记录的 MiniMax Chat 流
		{name: "tags split across chunks", chunks: []string{"<th", "ink>\nThe user greets me", ". Reply briefly.\n</thi", "nk>\n\nHello!"}, wantReasoning: "\nThe user greets me. Reply briefly.\n", wantContent: "Hello!"},
		{name: "newlines after close in later chunk", chunks: []string{"<think>a</think>", "\n", "\nb"}, wantReasoning: "a", wantContent: "b"},
		{name: "later think tag stays content", chunks: []string{"<think>a</think>b <think>c</think>"}, wantReasoning: "a", wantContent: "b <think>c</think>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reasoning, content, unclosed := feedAll(&thinkTagSplitter{}, tt.chunks...)
			if reasoning != tt.wantReasoning || content != tt.wantContent || unclosed {
				t.Fatalf("got reasoning=%q content=%q unclosed=%t, want %q/%q/false", reasoning, content, unclosed, tt.wantReasoning, tt.wantContent)
			}
		})
	}
}

func TestThinkTagSplitterByteByByte(t *testing.T) {
	text := "  <think>step one</think>\nfinal"
	chunks := make([]string, 0, len(text))
	for i := 0; i < len(text); i++ {
		chunks = append(chunks, text[i:i+1])
	}
	reasoning, content, unclosed := feedAll(&thinkTagSplitter{}, chunks...)
	if reasoning != "step one" || content != "final" || unclosed {
		t.Fatalf("got reasoning=%q content=%q unclosed=%t", reasoning, content, unclosed)
	}
}

func TestThinkTagSplitterLeavesOtherTextUnchanged(t *testing.T) {
	for _, chunks := range [][]string{
		{"plain answer"},
		{"<thinking>not our tag</thinking>"},
		{"<th", "e end"},
		{"  "},
		{"<"},
		{"answer <think>x</think>"},
	} {
		reasoning, content, unclosed := feedAll(&thinkTagSplitter{}, chunks...)
		want := ""
		for _, chunk := range chunks {
			want += chunk
		}
		if reasoning != "" || content != want || unclosed {
			t.Errorf("chunks %q: got reasoning=%q content=%q unclosed=%t", chunks, reasoning, content, unclosed)
		}
	}
}

func TestThinkTagSplitterUnclosedKeepsBufferedThinking(t *testing.T) {
	reasoning, content, unclosed := feedAll(&thinkTagSplitter{}, "<think>half a thought", " and a partial </thi")
	if reasoning != "half a thought and a partial </thi" || content != "" || !unclosed {
		t.Fatalf("got reasoning=%q content=%q unclosed=%t", reasoning, content, unclosed)
	}
}

func TestThinkTagSplitterIndependentReasoningDisablesSplit(t *testing.T) {
	s := &thinkTagSplitter{}
	if out := s.markIndependentReasoning(); out != (thinkSplit{}) {
		t.Fatalf("mark before content = %+v", out)
	}
	reasoning, content, _ := feedAll(s, "<think>quoted by the model</think>")
	if reasoning != "" || content != "<think>quoted by the model</think>" {
		t.Fatalf("got reasoning=%q content=%q", reasoning, content)
	}

	// 已缓冲的开头在出现独立推理字段时原样交还为正文
	s = &thinkTagSplitter{}
	if out := s.feed("<thi"); out != (thinkSplit{}) {
		t.Fatalf("partial open tag should be held, got %+v", out)
	}
	if out := s.markIndependentReasoning(); out.Content != "<thi" || out.Reasoning != "" {
		t.Fatalf("held lead after independent reasoning = %+v", out)
	}
}
