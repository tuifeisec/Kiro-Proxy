package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"kiro-proxy/converter"
)

// Synthetic fixtures preserve native shapes without copying user content.
const rawWorkflowCompatTools = `[
{"type":"function","name":"run_workflow","strict":false,"x-definition":9007199254740993,"parameters":{"type":"object","properties":{"workflowPath":{"type":["string","null"]},"workflowPrompt":{"type":["string","null"]},"inputs":{"type":"object","additionalProperties":{"type":"string"}},"runLabel":{"type":"string","pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$"}}}},
{"type":"function","name":"control_pwsh_process","strict":true,"parameters":{"type":"object","properties":{"command":{"anyOf":[{"anyOf":[{"not":{}},{"type":"string"}]},{"type":"null"}]},"large":{"type":"integer","const":9007199254740993}}}},
{"type":"function","name":"todo_list","strict":null,"parameters":{"type":"object","properties":{"tasks":{"type":["array","null"],"items":{"type":"object"}},"new_tasks":{"type":["array","null"],"items":{"$ref":"#/properties/tasks/anyOf/0/anyOf/1/items"}}}}},
{"type":"function","name":"run_workflow","parameters":{"type":"object","properties":{"label":{"type":"string","pattern":"near-match"}}}}
]`

func rawWorkflowCompatInput(t *testing.T) []byte {
	t.Helper()
	items := []map[string]string{
		{"type": "function_call", "call_id": "bad-call", "name": "run_workflow", "arguments": `{"workflowPath":"bundled://investigate","workflowPrompt":"synthetic","inputs":{"brief":"b","report_path":"r"}}`},
		{"type": "function_call_output", "call_id": "bad-call", "output": "Exactly one of \x60workflowPath\x60 or \x60workflowPrompt\x60 must be provided, not both."},
		{"type": "function_call", "call_id": "good-call", "name": "run_workflow", "arguments": `{"workflowPath":"bundled://investigate","inputs":{"brief":"b","report_path":"r"}}`},
		{"type": "function_call_output", "call_id": "good-call", "output": `{"status":"running","workflowId":"synthetic-workflow"}`},
		{"type": "function_call", "call_id": "child-tool", "name": "control_pwsh_process", "arguments": `{"command":"synthetic"}`},
		{"type": "function_call_output", "call_id": "child-tool", "output": "synthetic child result"},
	}
	result, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func rawWorkflowJSONValue(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func inspectRawWorkflowCompatRequest(t *testing.T, raw []byte, chat bool, originalInput []byte) {
	t.Helper()
	var request map[string]json.RawMessage
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(request["tools"], &tools); err != nil || len(tools) != 4 {
		t.Fatalf("tool count changed: %d, %v", len(tools), err)
	}
	names := []string{"run_workflow", "control_pwsh_process", "todo_list", "run_workflow"}
	strict := []string{"false", "true", "null", ""}
	for i, tool := range tools {
		definition := tool
		if chat {
			definition = nil
			if err := json.Unmarshal(tool["function"], &definition); err != nil {
				t.Fatal(err)
			}
		}
		var name string
		_ = json.Unmarshal(definition["name"], &name)
		if name != names[i] || string(definition["strict"]) != strict[i] {
			t.Fatalf("order/strict changed at %d: name=%q strict=%s", i, name, definition["strict"])
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   json.RawMessage            `json:"required"`
		}
		if err := json.Unmarshal(definition["parameters"], &schema); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if string(definition["x-definition"]) != "9007199254740993" {
				t.Fatal("extension precision lost")
			}
			var label map[string]string
			_ = json.Unmarshal(schema.Properties["runLabel"], &label)
			if (label["pattern"] == proxyObservedWorkflowPattern) != chat {
				t.Fatalf("wrong pattern profile: %q", label["pattern"])
			}
			for _, key := range []string{"workflowPath", "workflowPrompt", "inputs"} {
				if len(schema.Properties[key]) == 0 {
					t.Fatalf("native field lost: %s", key)
				}
			}
			if len(schema.Required) != 0 {
				t.Fatal("native optional fields made required")
			}
		}
		if i == 1 {
			if !bytes.Contains(schema.Properties["large"], []byte("9007199254740993")) {
				t.Fatal("schema precision lost")
			}
			if bytes.Contains(schema.Properties["command"], []byte(`"not"`)) == chat {
				t.Fatal("wrong false-branch profile")
			}
		}
		if i == 2 {
			var value struct {
				Items struct {
					Ref string `json:"$ref"`
				} `json:"items"`
			}
			_ = json.Unmarshal(schema.Properties["new_tasks"], &value)
			want := "#/properties/tasks/anyOf/0/anyOf/1/items"
			if chat {
				want = "#/properties/tasks/items"
			}
			if value.Items.Ref != want {
				t.Fatalf("wrong reference profile: %q", value.Items.Ref)
			}
		}
	}
	if !chat {
		if !reflect.DeepEqual(rawWorkflowJSONValue(t, request["input"]), rawWorkflowJSONValue(t, originalInput)) {
			t.Fatal("native history changed")
		}
		return
	}
	var input []map[string]string
	_ = json.Unmarshal(originalInput, &input)
	var messages []struct {
		Role       string `json:"role"`
		Content    string `json:"content"`
		ToolCallID string `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Function struct {
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	_ = json.Unmarshal(request["messages"], &messages)
	if len(messages) != len(input) {
		t.Fatalf("history length changed: %d", len(messages))
	}
	for i, item := range input {
		if item["type"] == "function_call" {
			if messages[i].Role != "assistant" || len(messages[i].ToolCalls) != 1 || messages[i].ToolCalls[0].ID != item["call_id"] || messages[i].ToolCalls[0].Function.Arguments != item["arguments"] {
				t.Fatalf("call parameters changed at %d", i)
			}
		} else if messages[i].Role != "tool" || messages[i].ToolCallID != item["call_id"] || messages[i].Content != item["output"] {
			t.Fatalf("tool error/result changed at %d", i)
		}
	}
}

func TestRawResponsesWorkflowSchemaActualUpstream(t *testing.T) {
	for _, chat := range []bool{false, true} {
		for _, group := range []bool{false, true} {
			t.Run(fmt.Sprintf("chat=%v/group=%v", chat, group), func(t *testing.T) {
				input := rawWorkflowCompatInput(t)
				body := []byte(`{"model":"local-alias","stream":false,"input":` + string(input) + `,"tools":` + rawWorkflowCompatTools + `}`)
				original := append([]byte(nil), body...)
				var hits atomic.Int32
				captured := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					expected := "/v1/responses"
					if chat {
						expected = "/v1/chat/completions"
					}
					if r.URL.Path != expected {
						t.Errorf("wrong endpoint: %s", r.URL.Path)
						http.Error(w, "wrong endpoint", 400)
						return
					}
					actual, readErr := io.ReadAll(r.Body)
					select {
					case captured <- actual:
					default:
					}
					badProfile := !chat && bytes.Contains(actual, []byte(`\\p{Cc}`))
					badProfile = badProfile || chat && (bytes.Contains(actual, []byte(`"not"`)) || bytes.Contains(actual, []byte("#/properties/tasks/anyOf/0/anyOf/1/items")))
					if readErr != nil || badProfile || bytes.Count(actual, []byte("9007199254740993")) != 2 {
						http.Error(w, "invalid schema profile or numeric precision", http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if chat {
						_, _ = io.WriteString(w, `{"id":"synthetic-chat","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
					} else {
						_, _ = io.WriteString(w, `{"id":"synthetic-response","object":"response","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":2}}`)
					}
				}))
				defer server.Close()
				endpoint := "/responses"
				if chat {
					endpoint = "/chat/completions"
				}
				db := rawResponsesTestDB(t)
				forwarder, _ := rawResponsesTestForwarder(t, db)
				model := rawResponsesTestModel(server.URL, "openai", endpoint)
				req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", bytes.NewReader(body))
				pre := rawResponsesTestPre(db, model, req.URL.Path, string(body))
				route := rawResponsesTestRoute(model)
				if group {
					route = autoGroupRawRoute(model)
				}
				response, err := forwarder.ForwardRawResponses(req, pre, route, autoGroupMockLogger(t))
				if err != nil {
					t.Fatal(err)
				}
				_, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil || response.StatusCode != 200 || hits.Load() != 1 {
					t.Fatalf("forward failed/replayed: status=%d hits=%d err=%v", response.StatusCode, hits.Load(), readErr)
				}
				if !bytes.Equal(body, original) || !bytes.Equal(pre.Body, original) {
					t.Fatal("source body mutated")
				}
				inspectRawWorkflowCompatRequest(t, <-captured, chat, input)
			})
		}
	}
}

func TestRawResponsesWorkflowSchemaBoundaries(t *testing.T) {
	var source map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"tools":`+rawWorkflowCompatTools+`}`), &source)
	converted, err := responsesToolsToChat(source["tools"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(converted, []byte(`"not"`)) || !bytes.Contains(converted, []byte("9007199254740993")) {
		t.Fatal("shared converter changed raw schema or precision")
	}
	capture, err := stripRawWorkflowMetadata([]byte(`{"tools":` + rawWorkflowCompatTools + `,"metadata":{"kiro:proxy_continuation_id":"private","custom":"keep"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var captured map[string]json.RawMessage
	_ = json.Unmarshal(capture, &captured)
	if !reflect.DeepEqual(rawWorkflowJSONValue(t, captured["tools"]), rawWorkflowJSONValue(t, source["tools"])) {
		t.Fatal("capture changed native tools")
	}
	nested := []byte(`{"model":"alias","input":"synthetic","tools":[{"type":"function","x-outer":9007199254740993,"function":{"name":"control_pwsh_process","strict":false,"x-inner":9007199254740993,"parameters":{"properties":{"command":{"anyOf":[{"not":{}},{"type":"string"}]}}}}}]}`)
	prepared, err := prepareRawResponsesUpstreamBody(nested, nil, rawResponsesTestModel("http://localhost", "openai", "/chat/completions"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(prepared, []byte("9007199254740993")) != 2 || bytes.Contains(prepared, []byte(`"not"`)) || !bytes.Contains(prepared, []byte(`"strict":false`)) || !bytes.Contains(prepared, []byte(`"x-outer"`)) {
		t.Fatalf("nested fields/normalization lost: %s", prepared)
	}
	for _, helper := range []func(string, []byte) ([]byte, bool){
		func(name string, raw []byte) ([]byte, bool) {
			return converter.NormalizeOpenAIResponsesToolParameters(name, raw)
		},
		func(name string, raw []byte) ([]byte, bool) {
			return converter.NormalizeOpenAIChatToolParameters(name, raw)
		},
	} {
		bad := []byte(`{"pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$","anyOf":[{"not":{}},{"type":"string"}]} {}`)
		result, changed := helper("run_workflow", bad)
		if changed || !bytes.Equal(result, bad) {
			t.Fatal("malformed schema silently rewritten")
		}
	}
}
