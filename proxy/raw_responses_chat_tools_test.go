package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRawResponsesChatInterleavedToolsRemainSeparateForKiro(t *testing.T) {
	// The installed Kiro consumer associates all argument deltas with the
	// most recently added call rather than looking up delta.item_id.
	chunks := []string{
		`{"id":"chat-multi","model":"chat-model","choices":[{"delta":{"content":"working","tool_calls":[{"index":0,"id":"call-a","function":{"name":"first","arguments":"{\"a\":"}},{"index":1,"id":"call-b","function":{"name":"second","arguments":"{\"b\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}},{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
	}
	var upstream strings.Builder
	for _, chunk := range chunks {
		upstream.WriteString("data: " + chunk + "\n\n")
	}
	upstream.WriteString("data: [DONE]\n\n")
	recorder := httptest.NewRecorder()
	usage, _, assistant, err := copyChatStreamAsResponsesWithState(context.Background(), recorder, strings.NewReader(upstream.String()))
	if err != nil {
		t.Fatal(err)
	}
	current := ""
	arguments := map[string]string{}
	names := map[string]string{}
	done := map[string]bool{}
	completed := false
	scanner := bufio.NewScanner(strings.NewReader(recorder.Body.String()))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			Item  struct {
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case "response.output_item.added":
			if current != "" && !done[current] {
				t.Fatalf("second call started before the previous call ended: %s", recorder.Body.String())
			}
			current = event.Item.CallID
			names[current] = event.Item.Name
		case "response.function_call_arguments.delta":
			if current == "" || done[current] {
				t.Fatal("argument delta without an open call")
			}
			arguments[current] += event.Delta
		case "response.output_item.done":
			if current != event.Item.CallID {
				t.Fatal("tool done did not match the current Kiro call")
			}
			done[current] = true
		case "response.completed":
			completed = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if arguments["call-a"] != `{"a":1}` || arguments["call-b"] != `{"b":2}` || names["call-a"] != "first" || names["call-b"] != "second" || !done["call-a"] || !done["call-b"] || !completed {
		t.Fatalf("Kiro received mixed or incomplete tool calls: args=%v names=%v done=%v completed=%v", arguments, names, done, completed)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 3 || len(assistant["tool_calls"].([]interface{})) != 2 {
		t.Fatalf("usage or assistant tool state changed: usage=%+v assistant=%+v", usage, assistant)
	}
}

func TestRawResponsesChatGroupFailureDoesNotReplayOrReportSuccess(t *testing.T) {
	for _, upstreamBody := range []string{
		"data: {\"error\":{\"message\":\"PRIVATE_UPSTREAM_MESSAGE\"}}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
	} {
		t.Run(upstreamBody, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/chat/completions" {
					http.Error(w, "unexpected endpoint", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, upstreamBody)
			}))
			defer upstream.Close()
			db := rawResponsesTestDB(t)
			f, stats := rawResponsesTestForwarder(t, db)
			first, second := autoGroupRawModels()
			first.BaseURL, first.Endpoint = upstream.URL, "/chat/completions"
			second.BaseURL = upstream.URL
			route := autoGroupRawRoute(first, second)
			body := `{"model":"auto","stream":true,"input":[{"role":"user","content":"test"}]}`
			req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(body))
			pre := rawResponsesTestPre(db, nil, req.URL.Path, body)
			resp, err := f.ForwardRawResponses(req, pre, route, autoGroupMockLogger(t))
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil || !strings.Contains(string(data), "response.failed") || strings.Contains(string(data), "response.completed") {
				t.Fatalf("failed Chat stream became a success: body=%s err=%v", data, readErr)
			}
			rows := rawResponsesUsageItems(t, db)
			if calls.Load() != 1 || len(rows) != 1 || rows[0].Success || rows[0].ErrorMessage == "" || strings.Contains(rows[0].ErrorMessage, "PRIVATE_UPSTREAM_MESSAGE") {
				t.Fatalf("failure replayed or was recorded incorrectly: calls=%d usage=%+v", calls.Load(), rows)
			}
			if f.load.snapshot()[first.ID] != 0 || f.load.snapshot()[second.ID] != 0 || autoGroupHealthFailures(f, first) != 1 || stats.Snapshot().TotalErrors != 1 {
				t.Fatal("failed Chat stream leaked capacity or changed group accounting")
			}
		})
	}
}

func TestRawResponsesChatEmptyTerminalDoesNotReportSuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "bare DONE", body: "data: [DONE]\n\n"},
		{name: "invalid JSON before DONE", body: "data: {invalid}\n\ndata: [DONE]\n\n"},
		{name: "unknown JSON before DONE", body: "data: {\"unknown\":true}\n\ndata: [DONE]\n\n"},
		{name: "role-only choice before DONE", body: "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: [DONE]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			_, _, _, err := copyChatStreamAsResponsesWithState(context.Background(), recorder, strings.NewReader(tc.body))
			if err == nil || !strings.Contains(recorder.Body.String(), "response.failed") || strings.Contains(recorder.Body.String(), "response.completed") {
				t.Fatalf("empty or invalid Chat stream became a success: body=%s err=%v", recorder.Body.String(), err)
			}
		})
	}
}
