package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kiro-proxy/converter"
)

func TestForwardRawResponsesChatContinuationGroupsParallelToolCalls(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		roles    []string
		ids      [][]string
		contents []string
	}{
		{
			name:  "parallel calls with reversed results",
			input: `[{"type":"function_call","call_id":"call-a","name":"lookup_a","arguments":"{\"key\":\"a\"}"},{"type":"function_call","call_id":"call-b","name":"lookup_b","arguments":"{\"key\":\"b\"}"},{"type":"function_call_output","call_id":"call-b","output":"result-b"},{"type":"function_call_output","call_id":"call-a","output":"result-a"}]`,
			roles: []string{"assistant", "tool", "tool"}, ids: [][]string{{"call-a", "call-b"}, {"call-b"}, {"call-a"}}, contents: []string{"", "result-b", "result-a"},
		},
		{
			name:  "multiple complete tool rounds",
			input: `[{"type":"function_call","call_id":"call-a","name":"lookup_a","arguments":"{\"key\":\"a\"}"},{"type":"function_call","call_id":"call-b","name":"lookup_b","arguments":"{\"key\":\"b\"}"},{"type":"function_call_output","call_id":"call-b","output":"result-b"},{"type":"function_call_output","call_id":"call-a","output":"result-a"},{"type":"function_call","call_id":"call-c","name":"lookup_c","arguments":"{\"key\":\"c\"}"},{"type":"function_call_output","call_id":"call-c","output":"result-c"}]`,
			roles: []string{"assistant", "tool", "tool", "assistant", "tool"}, ids: [][]string{{"call-a", "call-b"}, {"call-b"}, {"call-a"}, {"call-c"}, {"call-c"}}, contents: []string{"", "result-b", "result-a", "", "result-c"},
		},
		{
			name:  "single tool call",
			input: `[{"type":"function_call","call_id":"call-a","name":"lookup_a","arguments":"{\"key\":\"a\"}"},{"type":"function_call_output","call_id":"call-a","output":"result-a"}]`,
			roles: []string{"assistant", "tool"}, ids: [][]string{{"call-a"}, {"call-a"}}, contents: []string{"", "result-a"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var outboundBody []byte
			var authorization string
			var requests int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/v1/chat/completions" {
					http.Error(w, "only Chat Completions is available", http.StatusNotFound)
					return
				}
				outboundBody, _ = io.ReadAll(r.Body)
				authorization = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"chatcmpl-continuation","model":"configured-gpt","choices":[{"message":{"role":"assistant","content":"tool results received"},"finish_reason":"stop"}],"usage":{"prompt_tokens":31,"completion_tokens":7}}`)
			}))
			defer upstream.Close()

			db := rawResponsesTestDB(t)
			f, _ := rawResponsesTestForwarder(t, db)
			model := rawResponsesTestModel(upstream.URL, "openai", "/chat/completions")
			model.ModelID = "configured-gpt"
			requestBody := `{"model":"local-alias","stream":false,"input":` + tc.input + `,"tools":[{"type":"function","name":"lookup_a","description":"lookup A","parameters":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}},{"type":"function","name":"lookup_b","description":"lookup B","parameters":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}},{"type":"function","name":"lookup_c","description":"lookup C","parameters":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}}],"metadata":{"kiro:conversation_id":"conversation-private","kiro:agent_mode":"spec","kiro:proxy_continuation_id":"continuation-private","kiro:proxy_current_tool_results":["result-a","result-b"]}}`
			req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(requestBody))
			pre := rawResponsesTestPre(db, model, req.URL.Path, requestBody)
			pre.Conv = &converter.ParsedConversation{ConversationID: "conversation-private"}
			resp, err := f.ForwardRawResponses(req, pre, rawResponsesTestRoute(model), nil)
			if err != nil {
				t.Fatalf("forward Responses continuation to Chat upstream: %v", err)
			}
			responseBody, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if readErr != nil || closeErr != nil {
				t.Fatalf("read converted response: read=%v close=%v", readErr, closeErr)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("downstream status = %d", resp.StatusCode)
			}
			if requests != 1 {
				t.Fatalf("upstream request count = %d, want 1 (no replay)", requests)
			}
			if authorization != "Bearer test-secret" {
				t.Fatalf("Authorization = %q", authorization)
			}
			var chatRequest struct {
				Model    string `json:"model"`
				Messages []struct {
					Role       string `json:"role"`
					Content    string `json:"content"`
					ToolCallID string `json:"tool_call_id"`
					ToolCalls  []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"messages"`
				Tools []struct {
					Type     string `json:"type"`
					Function struct {
						Name        string         `json:"name"`
						Description string         `json:"description"`
						Parameters  map[string]any `json:"parameters"`
					} `json:"function"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(outboundBody, &chatRequest); err != nil {
				t.Fatalf("decode Chat request %s: %v", outboundBody, err)
			}
			if chatRequest.Model != "configured-gpt" {
				t.Fatalf("upstream model = %q", chatRequest.Model)
			}
			if len(chatRequest.Messages) != len(tc.roles) {
				t.Fatalf("messages = %+v, want roles %v", chatRequest.Messages, tc.roles)
			}
			for i, message := range chatRequest.Messages {
				if message.Role != tc.roles[i] {
					t.Fatalf("message %d role = %q, want %q (%+v)", i, message.Role, tc.roles[i], chatRequest.Messages)
				}
				var gotIDs []string
				if message.Role == "assistant" {
					for _, call := range message.ToolCalls {
						gotIDs = append(gotIDs, call.ID)
					}
				} else {
					gotIDs = append(gotIDs, message.ToolCallID)
				}
				if strings.Join(gotIDs, ",") != strings.Join(tc.ids[i], ",") {
					t.Fatalf("message %d tool IDs = %v, want %v; messages=%+v", i, gotIDs, tc.ids[i], chatRequest.Messages)
				}
				if message.Role == "tool" && message.Content != tc.contents[i] {
					t.Fatalf("tool result for %s = %q, want %q", message.ToolCallID, message.Content, tc.contents[i])
				}
			}
			if len(chatRequest.Tools) != 3 || chatRequest.Tools[0].Type != "function" || chatRequest.Tools[0].Function.Name != "lookup_a" || chatRequest.Tools[0].Function.Description != "lookup A" || chatRequest.Tools[0].Function.Parameters["type"] != "object" || chatRequest.Tools[1].Function.Name != "lookup_b" || chatRequest.Tools[2].Function.Name != "lookup_c" {
				t.Fatalf("function tool schema was not preserved: %+v", chatRequest.Tools)
			}
			for _, privateField := range [][]byte{[]byte(`"metadata"`), []byte("kiro:"), []byte("conversation-private"), []byte("continuation-private")} {
				if bytes.Contains(outboundBody, privateField) {
					t.Errorf("Kiro metadata %s reached Chat upstream: %s", privateField, outboundBody)
				}
			}
			var response map[string]json.RawMessage
			if err := json.Unmarshal(responseBody, &response); err != nil {
				t.Fatalf("decode converted Responses body: %v", err)
			}
			var status string
			if err := json.Unmarshal(response["status"], &status); err != nil || status != "completed" {
				t.Fatalf("converted response status = %q, err=%v", status, err)
			}
			rows := rawResponsesUsageItems(t, db)
			if len(rows) != 1 || !rows[0].Success || rows[0].InputTokens != 31 || rows[0].OutputTokens != 7 {
				t.Fatalf("continuation usage = %+v", rows)
			}
			if f.load.snapshot()[model.ID] != 0 {
				t.Fatalf("model lease remains held after response read: %v", f.load.snapshot())
			}
		})
	}
}

func TestForwardRawResponsesChatIncompleteFinishReasons(t *testing.T) {
	for _, tc := range []struct {
		name         string
		finishReason string
		wantReason   string
		stream       bool
		group        bool
	}{
		{name: "JSON length", finishReason: "length", wantReason: "max_output_tokens"},
		{name: "JSON content filter", finishReason: "content_filter", wantReason: "content_filter"},
		{name: "JSON length group", finishReason: "length", wantReason: "max_output_tokens", group: true},
		{name: "JSON content filter group", finishReason: "content_filter", wantReason: "content_filter", group: true},
		{name: "stream length", finishReason: "length", wantReason: "max_output_tokens", stream: true},
		{name: "stream length group", finishReason: "length", wantReason: "max_output_tokens", stream: true, group: true},
		{name: "stream content filter", finishReason: "content_filter", wantReason: "content_filter", stream: true},
		{name: "stream content filter group", finishReason: "content_filter", wantReason: "content_filter", stream: true, group: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/v1/chat/completions" {
					http.Error(w, "only Chat Completions is available", http.StatusNotFound)
					return
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w,
						"data: {\"id\":\"chatcmpl-incomplete\",\"model\":\"configured-gpt\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-partial\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"part\\\":\"}}]}}]}\n\n"+
							"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\""+tc.finishReason+"\"}],\"usage\":{\"prompt_tokens\":13,\"completion_tokens\":2}}\n\n"+
							"data: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				body, _ := json.Marshal(map[string]interface{}{
					"id": "chatcmpl-incomplete", "model": "configured-gpt",
					"choices": []interface{}{map[string]interface{}{
						"message": map[string]interface{}{"role": "assistant", "tool_calls": []interface{}{map[string]interface{}{
							"id": "call-partial", "type": "function", "function": map[string]interface{}{"name": "lookup", "arguments": `{"part":`},
						}}}, "finish_reason": tc.finishReason,
					}},
					"usage": map[string]int{"prompt_tokens": 13, "completion_tokens": 2},
				})
				_, _ = w.Write(body)
			}))
			defer upstream.Close()

			db := rawResponsesTestDB(t)
			f, _ := rawResponsesTestForwarder(t, db)
			model := rawResponsesTestModel(upstream.URL, "openai", "/chat/completions")
			model.ModelID = "configured-gpt"
			requestBody := `{"model":"local-alias","stream":` + map[bool]string{true: "true", false: "false"}[tc.stream] + `,"input":"continue"}`
			req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(requestBody))
			pre := rawResponsesTestPre(db, model, req.URL.Path, requestBody)
			pre.Conv = &converter.ParsedConversation{ConversationID: "incomplete-group-session"}
			route := rawResponsesTestRoute(model)
			if tc.group {
				route = autoGroupRawRoute(model)
			}
			resp, err := f.ForwardRawResponses(req, pre, route, autoGroupMockLogger(t))
			if err != nil {
				t.Fatalf("forward Chat response: %v", err)
			}
			payload, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				t.Fatalf("read adapted incomplete response: %v; body=%s", readErr, payload)
			}
			if requests != 1 {
				t.Fatalf("incomplete response caused %d upstream requests, want one without replay", requests)
			}
			if !bytes.Contains(payload, []byte(`"status":"incomplete"`)) || !bytes.Contains(payload, []byte(`"reason":"`+tc.wantReason+`"`)) {
				t.Fatalf("finish_reason %q did not produce incomplete response with reason %q: %s", tc.finishReason, tc.wantReason, payload)
			}
			if !bytes.Contains(payload, []byte(`"type":"function_call"`)) || !bytes.Contains(payload, []byte(`"call_id":"call-partial"`)) || bytes.Contains(payload, []byte(`"status":"completed"`)) {
				t.Fatalf("partial function-call arguments were marked completed: %s", payload)
			}
			if got := incompleteFunctionCallStatus(t, payload, tc.stream); got != "incomplete" {
				t.Fatalf("partial function-call item status = %q, want incomplete: %s", got, payload)
			}
			if tc.stream && bytes.Contains(payload, []byte("response.completed")) {
				t.Fatalf("Chat finish_reason %q emitted a completed event: %s", tc.finishReason, payload)
			}
			rows := rawResponsesUsageItems(t, db)
			if len(rows) != 1 || rows[0].Success || rows[0].InputTokens != 13 || rows[0].OutputTokens != 2 {
				t.Fatalf("incomplete response usage = %+v", rows)
			}
			if requests := f.load.snapshot(); requests[model.ID] != 0 {
				t.Fatalf("model lease remains held after incomplete response: %v", requests)
			}
			if tc.group && f.affinity.get(route.Group.ID, "incomplete-group-session", time.Now()) != "" {
				t.Fatal("incomplete group response created model affinity")
			}
		})
	}
}

func incompleteFunctionCallStatus(t *testing.T, payload []byte, stream bool) string {
	t.Helper()
	if !stream {
		var response struct {
			Output []struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Status string `json:"status"`
			} `json:"output"`
		}
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatalf("decode incomplete JSON response: %v", err)
		}
		for _, item := range response.Output {
			if item.Type == "function_call" && item.CallID == "call-partial" {
				return item.Status
			}
		}
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Status string `json:"status"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			t.Fatalf("decode incomplete SSE event: %v", err)
		}
		if event.Type == "response.output_item.done" && event.Item.Type == "function_call" && event.Item.CallID == "call-partial" {
			return event.Item.Status
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan incomplete SSE response: %v", err)
	}
	return ""
}
