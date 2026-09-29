package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func TestCaptureOpenAIChatToolParameters(t *testing.T) {
	body := []byte(`{"messages":[{"content":"private prompt"}],"api_key":"private key","tools":[{"type":"function","function":{"name":"read_file","description":"private description","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`)
	directory := t.TempDir()
	path, err := captureOpenAIChatToolParameters(body, "test-trace", 1, directory)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private prompt", "private description", "private key"} {
		if strings.Contains(string(stored), forbidden) {
			t.Errorf("capture leaks %q", forbidden)
		}
	}
	var captured struct {
		TraceID string `json:"trace_id"`
		Attempt int    `json:"attempt"`
		Tools   []struct {
			Index      int             `json:"index"`
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(stored, &captured); err != nil {
		t.Fatal(err)
	}
	if captured.TraceID != "test-trace" || captured.Attempt != 1 || len(captured.Tools) != 1 || captured.Tools[0].Name != "read_file" || captured.Tools[0].Index != 0 {
		t.Fatalf("unexpected capture identity: %+v", captured)
	}
	if string(captured.Tools[0].Parameters) != `{"type":"object","properties":{"path":{"type":"string"}}}` {
		t.Fatalf("unexpected schema: %s", captured.Tools[0].Parameters)
	}
	secondPath, err := captureOpenAIChatToolParameters(body, "test-trace", 2, directory)
	if err != nil || secondPath == path {
		t.Fatalf("second attempt should have a distinct capture, path = %q, error = %v", secondPath, err)
	}
}

func TestSummarizeOpenAIChatTools(t *testing.T) {
	body := []byte(`{"messages":[{"content":"private prompt"}],"tools":[{"type":"function","function":{"name":"read_file","description":"private description","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}},{"type":"function","function":{"name":"without_parameters"}},{"type":"web_search","description":"unknown tool"}]}`)

	got, err := summarizeOpenAIChatTools(body)
	if err != nil {
		t.Fatalf("summarizeOpenAIChatTools() error = %v", err)
	}
	if got.ToolCount != 3 {
		t.Fatalf("tool count = %d, want 3", got.ToolCount)
	}
	if got.ToolsSHA256 != digest(`[{"type":"function","function":{"name":"read_file","description":"private description","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}},{"type":"function","function":{"name":"without_parameters"}},{"type":"web_search","description":"unknown tool"}]`) {
		t.Fatalf("tools hash = %q", got.ToolsSHA256)
	}
	if len(got.Tools) != 3 {
		t.Fatalf("tool details = %d, want 3", len(got.Tools))
	}

	first := got.Tools[0]
	parameters := `{"type":"object","properties":{"path":{"type":"string"}}}`
	if first.Index != 0 || first.FunctionName != "read_file" {
		t.Fatalf("first identity = (%d, %q)", first.Index, first.FunctionName)
	}
	if first.DefinitionSHA256 != digest(`{"type":"function","function":{"name":"read_file","description":"private description","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}`) {
		t.Fatalf("first definition hash = %q", first.DefinitionSHA256)
	}
	if first.ParametersSHA256 != digest(parameters) || first.ParametersBytes != len(parameters) || !first.ParametersTopLevelObject {
		t.Fatalf("first parameters summary = %+v", first)
	}

	missingParameters := got.Tools[1]
	if missingParameters.FunctionName != "without_parameters" || missingParameters.ParametersSHA256 != digest("") || missingParameters.ParametersBytes != 0 || missingParameters.ParametersTopLevelObject {
		t.Fatalf("missing parameters summary = %+v", missingParameters)
	}
	unknown := got.Tools[2]
	if unknown.Index != 2 || unknown.FunctionName != "" || unknown.DefinitionSHA256 != digest(`{"type":"web_search","description":"unknown tool"}`) {
		t.Fatalf("unknown tool summary = %+v", unknown)
	}

	serialized, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	for _, forbidden := range []string{"private prompt", "private description", "properties", "path", "unknown tool"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Errorf("summary leaks %q: %s", forbidden, serialized)
		}
	}
}

func TestSummarizeOpenAIChatToolsMissingAndEmpty(t *testing.T) {
	missing, err := summarizeOpenAIChatTools([]byte(`{"model":"test"}`))
	if err != nil {
		t.Fatalf("missing tools error = %v", err)
	}
	if missing.ToolCount != 0 || missing.ToolsSHA256 != "" || missing.Tools != nil {
		t.Fatalf("missing tools summary = %+v", missing)
	}

	empty, err := summarizeOpenAIChatTools([]byte(`{"tools":[]}`))
	if err != nil {
		t.Fatalf("empty tools error = %v", err)
	}
	if empty.ToolCount != 0 || empty.ToolsSHA256 != digest(`[]`) || empty.Tools == nil || len(empty.Tools) != 0 {
		t.Fatalf("empty tools summary = %+v", empty)
	}
}

func TestSummarizeOpenAIChatToolsTopLevelParametersMustBeObject(t *testing.T) {
	for _, parameters := range []string{`[]`, `null`, `"object"`, `42`} {
		t.Run(parameters, func(t *testing.T) {
			body := []byte(`{"tools":[{"type":"function","function":{"name":"f","parameters":` + parameters + `}}]}`)
			got, err := summarizeOpenAIChatTools(body)
			if err != nil {
				t.Fatalf("summarizeOpenAIChatTools() error = %v", err)
			}
			tool := got.Tools[0]
			if tool.ParametersSHA256 != digest(parameters) || tool.ParametersBytes != len(parameters) || tool.ParametersTopLevelObject {
				t.Fatalf("parameters summary = %+v", tool)
			}
		})
	}
}

func TestSummarizeOpenAIChatToolsRetainsUnknownDefinitions(t *testing.T) {
	got, err := summarizeOpenAIChatTools([]byte(`{"tools":[null,42,{"function":{"name":"without_type"}}]}`))
	if err != nil {
		t.Fatalf("summarizeOpenAIChatTools() error = %v", err)
	}
	if got.ToolCount != 3 || len(got.Tools) != 3 {
		t.Fatalf("summary = %+v", got)
	}
	wantHashes := []string{digest(`null`), digest(`42`), digest(`{"function":{"name":"without_type"}}`)}
	for i, tool := range got.Tools {
		if tool.Index != i || tool.FunctionName != "" || tool.DefinitionSHA256 != wantHashes[i] {
			t.Errorf("tool %d summary = %+v", i, tool)
		}
	}
}

func TestSummarizeOpenAIChatToolsRejectsInvalidInput(t *testing.T) {
	for _, body := range []string{
		`{`,
		`null`,
		`[]`,
		`{"tools":null}`,
		`{"tools":{}}`,
		`{"tools":"[]"}`,
	} {
		t.Run(body, func(t *testing.T) {
			if _, err := summarizeOpenAIChatTools([]byte(body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestSummarizeOpenAIChatToolsLargeCollection(t *testing.T) {
	const count = 512
	var builder strings.Builder
	builder.WriteString(`{"tools":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"type":"function","function":{"name":"tool"}}`)
	}
	builder.WriteString(`]}`)

	got, err := summarizeOpenAIChatTools([]byte(builder.String()))
	if err != nil {
		t.Fatalf("summarizeOpenAIChatTools() error = %v", err)
	}
	if got.ToolCount != count || len(got.Tools) != count || got.Tools[0].Index != 0 || got.Tools[count-1].Index != count-1 {
		t.Fatalf("large tool collection summary: count=%d details=%d", got.ToolCount, len(got.Tools))
	}
}

func TestSummarizeOpenAIChatToolsRecordsSchemaStructureOnly(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","function":{"name":"example","parameters":{"type":"object","properties":{"secret_field":{"anyOf":[{"not":{}},{"type":"string","description":"SECRET_DESCRIPTION"}]},"nullable":{"type":["string","null"]}},"additionalProperties":false}}}]}`)
	summary, err := summarizeOpenAIChatTools(body)
	if err != nil {
		t.Fatal(err)
	}
	features := summary.Tools[0].SchemaFeatures
	if features["anyOf"] != 1 || features["not"] != 1 || features["always_false_schema"] != 1 ||
		features["type_array"] != 1 || features["additionalProperties"] != 1 {
		t.Fatalf("schema structure not recorded: %#v", features)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret_field") || strings.Contains(string(encoded), "SECRET_DESCRIPTION") {
		t.Fatalf("schema summary leaked contents: %s", encoded)
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
