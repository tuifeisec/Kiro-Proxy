package proxy

import (
	"bytes"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func TestPrepareRawResponsesAnthropicBodySupportsResponsesInputBlocks(t *testing.T) {
	body := []byte(`{"model":"local","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_image","image_url":"data:image/png;base64,abc"}]}]}`)
	prepared, err := prepareRawResponsesAnthropicBody(body, nil, rawResponsesTestModel("https://example.test", "anthropic", "/messages"))
	if err != nil {
		t.Fatalf("prepare Anthropic request: %v", err)
	}
	if !bytes.Contains(prepared, []byte(`"type":"text"`)) || !bytes.Contains(prepared, []byte(`"type":"image"`)) {
		t.Fatalf("converted input blocks missing: %s", prepared)
	}
}

func TestConvertAnthropicJSONResponseToolArgumentsAndCacheWrite(t *testing.T) {
	body := `{"id":"msg_1","model":"claude","content":[{"type":"tool_use","id":"call-1","name":"lookup","input":{"q":"x"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2,"cache_creation_input_tokens":3}}`
	converted, err := convertAnthropicJSONResponse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(converted, &response); err != nil {
		t.Fatal(err)
	}
	var output []map[string]interface{}
	if err := json.Unmarshal(response["output"], &output); err != nil {
		t.Fatal(err)
	}
	if got, ok := output[0]["arguments"].(string); !ok || got != `{"q":"x"}` {
		t.Fatalf("tool arguments = %#v", output[0]["arguments"])
	}
	if !bytes.Contains(response["usage"], []byte(`"cache_write_tokens":3`)) || !bytes.Contains(response["usage"], []byte(`"cache_creation_input_tokens":3`)) {
		t.Fatalf("cache write usage missing: %s", response["usage"])
	}
}

func TestCopyAnthropicStreamAsResponsesOrdersToolsAndPreservesError(t *testing.T) {
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"model\":\"claude\",\"usage\":{\"input_tokens\":1}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-2\",\"name\":\"second\"}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-1\",\"name\":\"first\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"x\\\":1}\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2,\"cache_creation_input_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	var out bytes.Buffer
	if err := copyAnthropicStreamAsResponses(&out, strings.NewReader(stream)); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	doneStart := strings.Index(text, "event: response.output_item.done")
	if doneStart < 0 {
		t.Fatalf("tool done events are missing: %s", text)
	}
	firstDone := text[doneStart:]
	secondDoneStart := strings.Index(firstDone[1:], "event: response.output_item.done")
	if secondDoneStart < 0 {
		t.Fatalf("second tool done event is missing: %s", text)
	}
	secondDoneStart++
	if !strings.Contains(firstDone[:secondDoneStart], `"call_id":"call-1"`) || !strings.Contains(firstDone[secondDoneStart:], `"call_id":"call-2"`) {
		t.Fatalf("tool done events are not ordered: %s", text)
	}
	if !strings.Contains(text, `"name":"first"`) || !strings.Contains(text, `"cache_write_tokens":3`) {
		t.Fatalf("converted stream missing tool or usage fields: %s", text)
	}
	var failed bytes.Buffer
	err := copyAnthropicStreamAsResponses(&failed, strings.NewReader("data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"slow down\"}}\n\n"))
	if err == nil || !strings.Contains(failed.String(), `"message":"slow down"`) || !strings.Contains(failed.String(), `"type":"rate_limit_error"`) {
		t.Fatalf("error event was not preserved: err=%v body=%s", err, failed.String())
	}
}
