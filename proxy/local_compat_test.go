package proxy

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCopyLocalCompatResponseEmitsResponsesFailureOnMissingTerminal(t *testing.T) {
	rr := httptest.NewRecorder()
	rr.Header().Set("Content-Type", "text/event-stream")
	stream := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"

	_, err := copyLocalCompatResponse(context.Background(), rr, strings.NewReader(stream), localCompatResponses)
	if err == nil {
		t.Fatal("expected missing terminal error")
	}
	if !errors.Is(err, errLocalCompatStreamMissingTerminal) {
		t.Fatalf("error = %v, want missing-terminal classification", err)
	}
	if !strings.Contains(rr.Body.String(), "event: response.failed") || !strings.Contains(rr.Body.String(), "upstream_stream_interrupted") {
		t.Fatalf("missing controlled Responses failure: %s", rr.Body.String())
	}
}

func TestCopyLocalCompatResponseEmitsAnthropicFailureOnMissingTerminal(t *testing.T) {
	rr := httptest.NewRecorder()
	rr.Header().Set("Content-Type", "text/event-stream")
	stream := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"

	_, err := copyLocalCompatResponse(context.Background(), rr, strings.NewReader(stream), localCompatMessages)
	if err == nil {
		t.Fatal("expected missing terminal error")
	}
	if !errors.Is(err, errLocalCompatStreamMissingTerminal) {
		t.Fatalf("error = %v, want missing-terminal classification", err)
	}
	if !strings.Contains(rr.Body.String(), "event: error") || !strings.Contains(rr.Body.String(), "upstream_stream_interrupted") {
		t.Fatalf("missing controlled Anthropic failure: %s", rr.Body.String())
	}
}

func TestCopyLocalCompatResponseAcceptsChatDone(t *testing.T) {
	rr := httptest.NewRecorder()
	rr.Header().Set("Content-Type", "text/event-stream")
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"

	_, err := copyLocalCompatResponse(context.Background(), rr, strings.NewReader(stream), localCompatChat)
	if err != nil {
		t.Fatalf("unexpected terminal error: %v", err)
	}
	if strings.Contains(rr.Body.String(), "upstream_stream_interrupted") {
		t.Fatalf("successful stream received controlled failure: %s", rr.Body.String())
	}
}

func TestCopyLocalCompatResponseDoesNotWriteFailureAfterClientCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rr := httptest.NewRecorder()
	rr.Header().Set("Content-Type", "text/event-stream")

	_, err := copyLocalCompatResponse(ctx, rr, strings.NewReader("data: {\"choices\":[]}\n\n"), localCompatChat)
	if err == nil {
		t.Fatal("expected missing terminal error")
	}
	if strings.Contains(rr.Body.String(), "upstream_stream_interrupted") {
		t.Fatalf("client cancellation must not append a controlled failure: %s", rr.Body.String())
	}
}

func TestLocalCompatConversationIDPrefersExplicitIdentifiers(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/responses", nil)
	request.Header.Set("X-Session-ID", " session-header ")
	body := []byte(`{"prompt_cache_key":"cache-key","conversation_id":"body-session","instructions":"stable"}`)

	if got := localCompatConversationID(request, body, localCompatResponses); got != "session-header" {
		t.Fatalf("expected session header, got %q", got)
	}

	request.Header.Del("X-Session-ID")
	if got := localCompatConversationID(request, body, localCompatResponses); got != "body-session" {
		t.Fatalf("expected conversation ID before shared prompt cache key, got %q", got)
	}
}

func TestLocalCompatImageStatsRecognizesProviderFormats(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,YWJj"}}]},
			{"content":[{"type":"image","source":{"type":"base64","data":"YQ=="}}]}
		],
		"input":[{"type":"message","content":[{"type":"input_image","image_url":"https://example.test/image.png"}]}]
	}`)
	count, bytes := localCompatImageStats(body)
	if count != 3 || bytes != 4 {
		t.Fatalf("image stats = (%d, %d), want (3, 4)", count, bytes)
	}
}

func TestLocalCompatConversationIDUsesStableChatPrefix(t *testing.T) {
	first := []byte(`{
		"model":"group",
		"messages":[{"role":"system","content":"be concise"},{"role":"user","content":"first question"}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]
	}`)
	second := []byte(`{
		"tools":[{"function":{"parameters":{"type":"object"},"name":"lookup"},"type":"function"}],
		"messages":[{"content":"be concise","role":"system"},{"role":"assistant","content":"answer"},{"role":"user","content":"next question"}],
		"model":"group"
	}`)

	firstID := localCompatConversationID(nil, first, localCompatChat)
	secondID := localCompatConversationID(nil, second, localCompatChat)
	if firstID == "" || firstID != secondID {
		t.Fatalf("stable chat prefix must preserve affinity: %q != %q", firstID, secondID)
	}
}

func TestLocalCompatConversationIDUsesStableResponsesPrefix(t *testing.T) {
	first := []byte(`{"model":"group","instructions":"be concise","tools":[{"type":"function","name":"lookup"}],"input":"first","previous_response_id":"resp_1"}`)
	second := []byte(`{"model":"group","instructions":"be concise","tools":[{"name":"lookup","type":"function"}],"input":"second","previous_response_id":"resp_2"}`)

	firstID := localCompatConversationID(nil, first, localCompatResponses)
	secondID := localCompatConversationID(nil, second, localCompatResponses)
	if firstID == "" || firstID != secondID {
		t.Fatalf("stable Responses prefix must preserve affinity: %q != %q", firstID, secondID)
	}
}

func TestLocalCompatConversationIDUsesStableAnthropicPrefix(t *testing.T) {
	first := []byte(`{"model":"group","system":"be concise","tools":[{"name":"lookup","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"first"}]}`)
	second := []byte(`{"model":"group","system":"be concise","tools":[{"input_schema":{"type":"object"},"name":"lookup"}],"messages":[{"role":"user","content":"second"}]}`)

	firstID := localCompatConversationID(nil, first, localCompatMessages)
	secondID := localCompatConversationID(nil, second, localCompatMessages)
	if firstID == "" || firstID != secondID {
		t.Fatalf("stable Messages prefix must preserve affinity: %q != %q", firstID, secondID)
	}
}

func TestLocalCompatConversationIDIgnoresPreviousResponseIDAlone(t *testing.T) {
	first := []byte(`{"model":"group","input":"first","previous_response_id":"resp_1"}`)
	second := []byte(`{"model":"group","input":"second","previous_response_id":"resp_2"}`)

	if got := localCompatConversationID(nil, first, localCompatResponses); got != "" {
		t.Fatalf("previous_response_id alone must not become an affinity key: %q", got)
	}
	if got := localCompatConversationID(nil, second, localCompatResponses); got != "" {
		t.Fatalf("changing previous_response_id must not create a new affinity key: %q", got)
	}
}

func TestLocalCompatConversationIDIgnoresEmptyStableFields(t *testing.T) {
	body := []byte(`{"model":"group","instructions":" ","tools":[],"input":"question","previous_response_id":"resp_1"}`)
	if got := localCompatConversationID(nil, body, localCompatResponses); got != "" {
		t.Fatalf("empty stable fields must not pin unrelated requests: %q", got)
	}
}
