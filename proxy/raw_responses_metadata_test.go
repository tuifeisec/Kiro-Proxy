package proxy

import (
	"bytes"
	"testing"

	json "github.com/goccy/go-json"
)

func TestRawResponsesUpstreamMetadataBoundary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata string
		want     string
	}{
		{"Kiro only", `{"kiro:conversation_id":"conv","kiro:agent_mode":"vibe","kiro:root_conversation_id":"root","kiro:agent_continuation_id":"agent","kiro:proxy_continuation_id":"branch","kiro:proxy_current_tool_results":1}`, ""},
		{"future Kiro field", `{"kiro:future_context":"local"}`, ""},
		{"empty", `{}`, ""},
		{"null", `null`, ""},
		{"custom only", `{"custom":{"value":900719925474099312345}}`, `{"custom":{"value":900719925474099312345}}`},
		{"mixed", `{"kiro:conversation_id":"conv","custom":{"value":900719925474099312345}}`, `{"custom":{"value":900719925474099312345}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"input":[],"extension":900719925474099312345,"metadata":` + tc.metadata + `}`)
			got, err := stripRawUpstreamKiroContext(body)
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(got, &root); err != nil {
				t.Fatal(err)
			}
			if string(root["metadata"]) != tc.want {
				t.Fatalf("metadata=%s want=%s", root["metadata"], tc.want)
			}
			if string(root["extension"]) != "900719925474099312345" || bytes.Contains(got, []byte("kiro:")) {
				t.Fatalf("outbound body lost precision or retained local metadata: %s", got)
			}
		})
	}
}

func TestRawResponsesUpstreamMetadataPreservesCaptureBoundary(t *testing.T) {
	const body = `{"input":[],"metadata":{"kiro:conversation_id":"conv","kiro:agent_mode":"vibe","kiro:proxy_continuation_id":"private","custom":"keep"}}`
	got, err := stripRawWorkflowMetadata([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"kiro:conversation_id":"conv"`)) || !bytes.Contains(got, []byte(`"kiro:agent_mode":"vibe"`)) || !bytes.Contains(got, []byte(`"custom":"keep"`)) || bytes.Contains(got, []byte("kiro:proxy_")) {
		t.Fatalf("capture or official passthrough metadata boundary changed: %s", got)
	}
}
