package proxy

import (
	"fmt"
	json "github.com/goccy/go-json"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestForwardResponsesDynamicContinuationPersistsBeforeEOF(t *testing.T) {
	received := make(chan converter.ResponsesRequest, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request converter.ResponsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		received <- request
		id := "response-one"
		if request.PreviousResponseID != "" {
			id = "response-two"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":%q}}\n\n", id)
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n")
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\"}}\n\n", id)
	}))
	defer upstream.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "integration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stats := store.NewStats(db)
	defer stats.Close()
	f := NewForwarder(stats, db)
	defer f.CloseIdleConnections()
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	model := &store.Model{ID: "model", Provider: "openai", ModelID: "upstream", BaseURL: upstream.URL, Endpoint: "/v1/responses", EnableContinuation: true}
	history := []converter.ParsedHistoryEntry{}
	for turn := 1; turn <= 2; turn++ {
		question := fmt.Sprintf("question-%d", turn)
		conv := &converter.ParsedConversation{ConversationID: "thread", SystemText: "stable policy\nHere is the last user message:\n" + question, History: history, CurrentContent: question}
		pre := &Preflight{Protocol: "kiro", Conv: conv, StartTime: time.Now()}
		pre.usage = newRequestUsageRecorder(db, pre.StartTime, model, "/generateAssistantResponse", "kiro")
		req, _ := http.NewRequest(http.MethodPost, "http://local.invalid/generateAssistantResponse", nil)
		resp, err := f.Forward(req, pre, &router.Route{Model: model}, logger)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		got := <-received
		if got.Instructions != conv.SystemText {
			t.Fatalf("lost current instructions: %+v", got)
		}
		if turn == 2 && (got.PreviousResponseID != "response-one" || len(got.Input) != 1 || got.Input[0].Content != question) {
			t.Fatalf("second request not delta: %+v", got)
		}
		// EOF 返回即能看到状态，无需 sleep 等待后台写入。
		state, err := db.GetResponsesSession(responsesContinuationStateKeyFor(pre, model, ""))
		if err != nil || state == nil || state.ResponseID == "" {
			t.Fatalf("state not visible at EOF: %+v %v", state, err)
		}
		history = append(history, converter.ParsedHistoryEntry{UserContent: question, HasAssistant: true, AssistantContent: "answer"})
	}
}
