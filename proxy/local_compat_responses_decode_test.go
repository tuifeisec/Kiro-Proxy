package proxy

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// Mistral 等上游把 delta.content 发成分段数组；按字符串解码会让整个 chunk 被丢弃。
func TestCopyChatStreamAsResponsesAcceptsSegmentedContent(t *testing.T) {
	body := chatTestStream(t,
		`{"id":"c1","model":"m","choices":[{"delta":{"content":[{"type":"text","text":"你好"},{"type":"text","text":"，世界"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"delta":{},"finish_reason":"stop"}]}`,
	)
	if !strings.Contains(body, `"delta":"你好，世界"`) {
		t.Fatalf("segmented content was dropped:\n%s", body)
	}
}

// 容错之后仍无法解析的 chunk 不能静默跳过：其中可能带工具调用或 finish_reason。
func TestCopyChatStreamAsResponsesFailsOnUnparseableChunk(t *testing.T) {
	for name, chunk := range map[string]string{
		"not json":         `{"id":"c1","choices":[`,
		"content is a map": `{"id":"c1","choices":[{"delta":{"content":{"text":"x"}}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			stream := "data: " + chunk + "\n\ndata: [DONE]\n\n"
			recorder := httptest.NewRecorder()
			_, _, _, err := copyChatStreamAsResponsesWithState(context.Background(), recorder, strings.NewReader(stream))
			if !errors.Is(err, errLocalCompatStreamProtocol) || !strings.Contains(err.Error(), "无法解析上游 Chat 流事件") {
				t.Fatalf("err = %v, want protocol error naming the unparseable event", err)
			}
			if !strings.Contains(recorder.Body.String(), "response.failed") {
				t.Fatalf("client was not told the stream failed:\n%s", recorder.Body.String())
			}
		})
	}
}
