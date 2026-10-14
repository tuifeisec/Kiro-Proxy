package proxy

import (
	"bytes"
	"testing"

	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func TestRawResponsesPrepareKiroContextBoundary(t *testing.T) {
	model := &store.Model{ModelID: "configured-model"}
	for _, tc := range []struct {
		name          string
		body          string
		wantMetadata  string
		wantReasoning string
	}{
		{
			name:          "mixed native context and caller extensions",
			body:          `{"model":"auto","input":[],"origin":"kiro","profileArn":"arn:example","reasoning":{"type":"enabled","effort":"medium","summary":"detailed","vendor_extension":{"flag":true}},"metadata":{"kiro:conversation_id":"conv","kiro:agent_mode":"vibe","kiro:proxy_continuation_id":"private","custom":{"large":900719925474099312345}},"extension":{"precise":900719925474099312345}}`,
			wantMetadata:  `{"custom":{"large":900719925474099312345}}`,
			wantReasoning: `{"effort":"medium","summary":"detailed","vendor_extension":{"flag":true}}`,
		},
		{
			name: "no metadata preserves absence",
			body: `{"model":"auto","input":[],"origin":"kiro","profileArn":"arn:example","extension":900719925474099312345}`,
		},
		{
			name: "type only reasoning is removed",
			body: `{"model":"auto","input":[],"reasoning":{"type":"enabled"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := prepareRawResponsesBody([]byte(tc.body), nil, model)
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(got, &root); err != nil {
				t.Fatalf("decode prepared body: %v body=%s", err, got)
			}
			for _, key := range []string{"origin", "profileArn"} {
				if _, exists := root[key]; exists {
					t.Errorf("Kiro context %q leaked upstream: %s", key, got)
				}
			}
			if string(root["model"]) != `"configured-model"` {
				t.Errorf("configured upstream model not applied: %s", root["model"])
			}
			if bytes.Contains([]byte(tc.body), []byte("900719925474099312345")) && !bytes.Contains(got, []byte("900719925474099312345")) {
				t.Errorf("large number lost precision: %s", got)
			}
			if tc.wantMetadata == "" {
				if _, exists := root["metadata"]; exists {
					t.Errorf("unexpected metadata was added: %s", root["metadata"])
				}
			} else if string(root["metadata"]) != tc.wantMetadata {
				t.Errorf("metadata=%s want=%s", root["metadata"], tc.wantMetadata)
			}
			if tc.wantReasoning == "" {
				if _, exists := root["reasoning"]; exists {
					t.Errorf("reasoning.type-only object was not removed: %s", root["reasoning"])
				}
			} else if string(root["reasoning"]) != tc.wantReasoning {
				t.Errorf("reasoning=%s want=%s", root["reasoning"], tc.wantReasoning)
			}
		})
	}
}

func TestRawResponsesCapturePreservesKiroContext(t *testing.T) {
	const body = `{"input":[],"origin":"kiro","profileArn":"arn:example","reasoning":{"type":"enabled","effort":"high","summary":"detailed"},"metadata":{"kiro:conversation_id":"conv","kiro:agent_mode":"vibe","kiro:proxy_continuation_id":"private","kiro:proxy_current_tool_results":1,"custom":"keep"}}`
	got, err := stripRawWorkflowMetadata([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatalf("decode capture body: %v body=%s", err, got)
	}
	if string(root["origin"]) != `"kiro"` || string(root["profileArn"]) != `"arn:example"` {
		t.Fatalf("capture changed native top-level context: %s", got)
	}
	var reasoning map[string]json.RawMessage
	if err := json.Unmarshal(root["reasoning"], &reasoning); err != nil {
		t.Fatalf("decode reasoning: %v", err)
	}
	if string(reasoning["type"]) != `"enabled"` || string(reasoning["effort"]) != `"high"` || string(reasoning["summary"]) != `"detailed"` {
		t.Fatalf("capture changed native reasoning context: %s", root["reasoning"])
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(root["metadata"], &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	for _, key := range []string{"kiro:proxy_continuation_id", "kiro:proxy_current_tool_results"} {
		if _, exists := metadata[key]; exists {
			t.Errorf("capture retained proxy-private metadata %q: %s", key, root["metadata"])
		}
	}
	for _, key := range []string{"kiro:conversation_id", "kiro:agent_mode", "custom"} {
		if _, exists := metadata[key]; !exists {
			t.Errorf("capture removed native/caller metadata %q: %s", key, root["metadata"])
		}
	}
}
