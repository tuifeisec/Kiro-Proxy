package proxy

import (
	"net/http/httptest"
	"strings"
	"testing"

	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func TestOpenAICompatDerivesBothNativeEndpoints(t *testing.T) {
	model := &store.Model{Provider: "openai", BaseURL: "https://upstream.example/v1", Endpoint: "/chat/completions"}
	chatURL, err := localCompatUpstreamURL(localCompatChat, model)
	if err != nil || chatURL != "https://upstream.example/v1/chat/completions" {
		t.Fatalf("chat URL: %q, %v", chatURL, err)
	}
	responsesURL, err := localCompatUpstreamURL(localCompatResponses, model)
	if err != nil || responsesURL != "https://upstream.example/v1/responses" {
		t.Fatalf("responses URL: %q, %v", responsesURL, err)
	}
	if responsesNeedsChatFallback(model) {
		t.Fatal("native OpenAI Responses should not use the Chat fallback")
	}
}

func TestResponsesToChatFallbackPreservesInputAndTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","instructions":"be concise","input":[{"type":"message","role":"user","content":"first"},{"type":"message","role":"assistant","content":"ok"},{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}],"tools":[{"type":"function","name":"lookup","description":"find","parameters":{"type":"object"}}],"stream":true}`)
	converted, state, err := responsesToChatFallback(body, "gpt-5.6-sol")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if state == nil || len(state.Messages) != 4 {
		t.Fatalf("unexpected state: %+v", state)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(converted, &got); err != nil {
		t.Fatalf("decode converted body: %v", err)
	}
	if got["prompt_cache_key"] == nil {
		t.Fatal("fallback request must carry a stable prompt cache key")
	}
	messages := got["messages"].([]interface{})
	if messages[0].(map[string]interface{})["role"] != "system" || messages[1].(map[string]interface{})["content"] != "first" {
		t.Fatalf("input order changed: %#v", messages)
	}
	tools := got["tools"].([]interface{})
	fn := tools[0].(map[string]interface{})["function"].(map[string]interface{})
	if fn["name"] != "lookup" {
		t.Fatalf("tool not converted: %#v", tools)
	}
}

func TestConvertChatCompletionToResponses(t *testing.T) {
	body := []byte(`{"id":"chatcmpl_1","model":"gpt-5.6-sol","created":123,"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":4,"total_tokens":104,"prompt_tokens_details":{"cached_tokens":80}}}`)
	converted, usage, err := convertChatCompletionToResponses(body)
	if err != nil {
		t.Fatalf("convert response: %v", err)
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 4 || usage.CacheReadTokens != 80 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(converted, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["id"] != "resp_chatcmpl_1" || response["output_text"] != "hello" {
		t.Fatalf("unexpected Responses response: %#v", response)
	}
}

func TestCopyChatStreamAsResponses(t *testing.T) {
	stream := "data: {\"id\":\"chatcmpl_2\",\"model\":\"gpt-5.6-sol\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl_2\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"prompt_tokens_details\":{\"cached_tokens\":8}}}\n\n" +
		"data: [DONE]\n\n"
	rr := httptest.NewRecorder()
	usage, err := copyChatStreamAsResponses(rr, strings.NewReader(stream))
	if err != nil {
		t.Fatalf("convert stream: %v", err)
	}
	if usage.InputTokens != 10 || usage.CacheReadTokens != 8 {
		t.Fatalf("unexpected stream usage: %+v", usage)
	}
	if !strings.Contains(rr.Body.String(), "response.output_text.delta") || !strings.Contains(rr.Body.String(), "response.completed") {
		t.Fatalf("missing Responses events: %s", rr.Body.String())
	}
}
