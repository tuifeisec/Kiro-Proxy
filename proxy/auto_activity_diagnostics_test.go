package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kiro-proxy/converter"
)

func newActivityDiagnosticLogger(t *testing.T) (*TrafficLogger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "traffic.log")
	logger, err := NewTrafficLogger(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)
	return logger, path + ".auto-diagnostic.log"
}

func readActivityDiagnosticEntries(t *testing.T, path string) []LogEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-body-marker", "secret-auth-marker", "sess_private", "private-continuation", "private-execution", "private-workflow"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("diagnostic leaked %q", forbidden)
		}
	}
	var entries []LogEntry
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var entry LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Detail != "" {
			t.Fatal("diagnostic copied body detail")
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestActivityDiagnosticCommitIgnoredAndFailure(t *testing.T) {
	logger, path := newActivityDiagnosticLogger(t)
	s := &Server{logger: logger}
	valid := `{"seq":2,"sessionId":"sess_private","content":{"secret":"secret-body-marker","executionId":"private-execution","_meta":{"kiro":{"workflow":{"workflowId":"private-workflow","nodeId":"plan","nodePath":["private-workflow","plan"]}}}}}`
	request := func(body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity", strings.NewReader(body))
		r.Header.Set("Authorization", "secret-auth-marker")
		return r
	}
	first, parseErr, consumeErr := s.discardKiroActivityRequestBody(request(valid))
	if parseErr != nil || consumeErr != nil || first.Committed != 1 || first.StoreIgnored != 0 {
		t.Fatalf("first report=%+v errors=%v/%v", first, parseErr, consumeErr)
	}
	replay, parseErr, consumeErr := s.discardKiroActivityRequestBody(request(valid))
	if parseErr != nil || consumeErr != nil || replay.Committed != 0 || replay.StoreIgnored != 1 {
		t.Fatalf("replay report=%+v errors=%v/%v", replay, parseErr, consumeErr)
	}
	_, _, _ = s.discardKiroActivityRequestBody(request(`{"sessionId":"sess_private","agentMode":"vibe","content":{"text":"secret-body-marker"}}`))
	_, _, _ = s.discardKiroActivityRequestBody(request(`{"secret":"secret-body-marker"`))
	failing := request("")
	failing.Body = &kiroActivityFailingBody{readErr: errors.New("secret-body-marker"), closeErr: errors.New("secret-auth-marker")}
	_, _, _ = s.discardKiroActivityRequestBody(failing)
	entries := readActivityDiagnosticEntries(t, path)
	if len(entries) != 5 {
		t.Fatalf("events=%d", len(entries))
	}
	if entries[0].Metadata["committed_sessions"] != float64(1) || entries[1].Metadata["store_ignored_sessions"] != float64(1) {
		t.Fatal("missing commit/ignored evidence")
	}
	if entries[2].Metadata["candidate_sessions"] != float64(0) || entries[2].Metadata["parse_ok"] != true {
		t.Fatal("ordinary activity misclassified")
	}
	if entries[3].Metadata["parse_ok"] != false || entries[3].Metadata["committed_sessions"] != float64(0) {
		t.Fatal("malformed batch claimed a commit")
	}
	if entries[4].Metadata["consume_ok"] != false || entries[4].Metadata["committed_sessions"] != float64(0) {
		t.Fatal("consume failure claimed a commit")
	}
}

func TestActivityLookupDiagnosticsTrackExistingLookupWithoutChangingRouting(t *testing.T) {
	logger, path := newActivityDiagnosticLogger(t)
	s := &Server{logger: logger}
	body := []byte(`{"model":"auto","input":[],"metadata":{"kiro:conversation_id":"sess_private","kiro:agent_mode":"vibe","kiro:agent_continuation_id":"private-continuation"}}`)
	request := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/v1/responses", nil)
	}
	if _, ok := s.synthesizeKiroActivityWorkflowHeader(request(), body); ok {
		t.Fatal("empty store synthesized")
	}
	s.recordKiroActivityRouting(kiroActivityRoutingMeta{SessionID: "sess_private", WorkflowID: "private-workflow", NodeID: "plan", NodePath: "plan", AgentMode: "wf-planner", Sequence: 1, HasSequence: true})
	if _, ok := s.synthesizeKiroActivityWorkflowHeader(request(), body); ok {
		t.Fatal("mode mismatch synthesized")
	}
	s.recordKiroActivityRouting(kiroActivityRoutingMeta{SessionID: "sess_private", WorkflowID: "private-workflow", NodeID: "plan", NodePath: "plan", AgentMode: "vibe", Sequence: 2, HasSequence: true})
	if _, ok := s.synthesizeKiroActivityWorkflowHeader(request(), body); !ok {
		t.Fatal("compatible observation not synthesized")
	}
	entries := readActivityDiagnosticEntries(t, path)
	if len(entries) != 3 {
		t.Fatalf("events=%d", len(entries))
	}
	for i, status := range []string{"no_observation", "mode_mismatch", "header_synthesized"} {
		if entries[i].Message != "activity_lookup" || entries[i].Metadata["status"] != status {
			t.Fatalf("event %d=%+v", i, entries[i])
		}
	}
	if _, ok := entries[0].Metadata["observation_age_ms"]; ok {
		t.Fatal("missing observation has invented age")
	}
	if entries[2].Metadata["sequence"] != float64(2) {
		t.Fatal("wrong observation sequence")
	}
	if _, ok := entries[2].Metadata["observation_age_ms"]; !ok {
		t.Fatal("missing age")
	}
}

func TestActivityDiagnosticSamplesBoundedAndRequestResultCorrelated(t *testing.T) {
	logger, path := newActivityDiagnosticLogger(t)
	s := &Server{logger: logger}
	report := kiroActivityParseReport{Entries: make([]kiroActivityRoutingMeta, 40)}
	for i := range report.Entries {
		report.Entries[i] = kiroActivityRoutingMeta{SessionID: strings.Repeat("a", i+1)}
	}
	s.logKiroActivityBatch(report, nil, nil)
	pre := &Preflight{SourceModel: "auto", StartTime: time.Now(), Conv: &converter.ParsedConversation{ConversationID: "sess_private", ContinuationStateKey: "private-continuation"}}
	s.logAutoRequestResult(pre, "forward_failed", http.StatusBadGateway)
	s.logAutoRequestResult(pre, "upstream_headers", http.StatusOK)
	manual := &Preflight{SourceModel: "manual", Conv: pre.Conv}
	s.logAutoRequestResult(manual, "upstream_headers", http.StatusOK)
	entries := readActivityDiagnosticEntries(t, path)
	if len(entries) != 3 {
		t.Fatalf("events=%d", len(entries))
	}
	if len(entries[0].Metadata["candidate_samples"].([]interface{})) != 16 || entries[0].Metadata["candidate_samples_omitted"] != float64(24) {
		t.Fatal("unbounded samples")
	}
	if entries[1].Metadata["request_id"] == "" || entries[1].Metadata["request_id"] != entries[2].Metadata["request_id"] {
		t.Fatal("request identity not stable")
	}
	if entries[1].Metadata["request_id"] == autoDiagnosticRequestKey(&Preflight{}) {
		t.Fatal("different requests share identity")
	}
	if entries[2].Metadata["phase"] != "upstream_headers" {
		t.Fatal("header arrival confused with stream completion")
	}
}

func TestAutoDiagnosticWriteFailureIsReportedInMainLog(t *testing.T) {
	logger, diagnosticPath := newActivityDiagnosticLogger(t)
	if err := logger.autoDiagnosticFile.Close(); err != nil {
		t.Fatal(err)
	}
	s := &Server{logger: logger}
	s.recordAutoDiagnostic("activity_batch", map[string]interface{}{"parse_ok": true})
	logger.Close()
	mainPath := strings.TrimSuffix(diagnosticPath, ".auto-diagnostic.log")
	data, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "写入 Auto 诊断日志失败") || !strings.Contains(string(data), `"level":"error"`) {
		t.Fatal("diagnostic failure was not explicitly reported")
	}
}
