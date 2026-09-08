package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

// 按实际 400 的字段约束模拟上游，同时检查摘要及工具调用没有被修复丢弃。
func TestResponsesSummaryReplayAcceptedByUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []struct {
				Type    string          `json:"type"`
				ID      string          `json:"id"`
				CallID  string          `json:"call_id"`
				Content json.RawMessage `json:"content"`
				Summary []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"summary"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for i, item := range req.Input {
			if item.Type != "reasoning" {
				continue
			}
			var content []json.RawMessage
			if len(item.Content) > 0 && json.Unmarshal(item.Content, &content) != nil {
				http.Error(w, "invalid reasoning content", http.StatusBadRequest)
				return
			}
			if len(content) > 0 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"error":{"param":"input[%d].content","code":"array_above_max_length"}}`, i)
				return
			}
		}
		if len(req.Input) != 12 || req.Input[9].Type != "reasoning" || req.Input[9].ID != "rs_summary" ||
			len(req.Input[9].Summary) != 1 || req.Input[9].Summary[0].Type != "summary_text" || req.Input[9].Summary[0].Text != "inspect the file" ||
			req.Input[10].Type != "function_call" || req.Input[10].CallID != "call_1" ||
			req.Input[11].Type != "function_call_output" || req.Input[11].CallID != "call_1" {
			http.Error(w, "lost summary or tool history", http.StatusUnprocessableEntity)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	post := func(t *testing.T, body []byte, url string) (int, []byte) {
		t.Helper()
		resp, err := server.Client().Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}

	// 证明模拟上游能识别原始错误，而非无条件返回成功。
	badItems := make([]map[string]interface{}, 10)
	for i := 0; i < 9; i++ {
		badItems[i] = map[string]interface{}{"role": "user", "content": "message"}
	}
	badItems[9] = map[string]interface{}{"type": "reasoning", "content": []map[string]string{{"type": "reasoning_text", "text": "inspect the file"}}}
	badBody, err := json.Marshal(map[string]interface{}{"input": badItems})
	if err != nil {
		t.Fatal(err)
	}
	if status, data := post(t, badBody, server.URL); status != http.StatusBadRequest || !bytes.Contains(data, []byte(`"param":"input[9].content"`)) {
		t.Fatalf("old payload: status=%d body=%s", status, data)
	}

	conv := &converter.ParsedConversation{ConversationID: "summary-replay"}
	for i := 0; i < 9; i++ {
		conv.History = append(conv.History, converter.ParsedHistoryEntry{HasAssistant: true, AssistantContent: "earlier message"})
	}
	conv.History = append(conv.History,
		converter.ParsedHistoryEntry{HasAssistant: true, ToolUses: []converter.ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}}},
		converter.ParsedHistoryEntry{ToolResults: []converter.ParsedToolResult{{ToolUseID: "call_1", Content: "package example"}}},
	)
	// 与已有持久化摘要的字段相同，无需迁移或清除旧会话。
	history := map[string]converter.ReasoningTrace{
		converter.ReasoningTurnKey([]string{"call_1"}): {
			Present: true, Protocol: "responses", WireField: "reasoning_summary_text",
			Text: "inspect the file", ItemID: "rs_summary",
		},
	}
	for _, provider := range []string{"codex", "openai"} {
		t.Run(provider, func(t *testing.T) {
			pre := &Preflight{Protocol: "kiro", Conv: conv}
			model := &store.Model{Provider: provider, BaseURL: server.URL, Endpoint: "/responses", ModelID: "test-model"}
			body, url, err := convertRequestWithProtocol(pre, model, history, false)
			if err != nil {
				t.Fatal(err)
			}
			if status, data := post(t, body, url); status != http.StatusOK {
				t.Fatalf("replay rejected: status=%d body=%s", status, data)
			}
		})
	}
}
