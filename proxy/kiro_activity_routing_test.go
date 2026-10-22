package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

// kiroActivityRoutingPayload 按契约样例结构生成 activity 正文；nodePathJSON 传 JSON 数组
// 字面量或 null，其余白名单外字段用于验证扫描器只提取白名单。
func kiroActivityRoutingPayload(sessionID, executionID, workflowID, workflowName, nodeID, nodePathJSON, agentMode string) string {
	return fmt.Sprintf(`{"sessionId":%q,"attachments":null,"content":{"sequenceId":7,"text":"payload noise","executionId":%q,"_meta":{"kiro":{"agentMode":%q,"workflow":{"workflowId":%q,"workflowName":%q,"nodeId":%q,"nodePath":%s,"type":"step"},"nested":{"deep":[true,null,"x"]}}}},"trailing":{"a":[1,2,{"b":null}]}}`,
		sessionID, executionID, agentMode, workflowID, workflowName, nodeID, nodePathJSON)
}

func kiroActivityBatchRoutingPayload(seq int64, sessionID, executionID, workflowID, workflowName, nodeID, nodePathJSON, agentMode string) string {
	event := kiroActivityRoutingPayload(sessionID, executionID, workflowID, workflowName, nodeID, nodePathJSON, agentMode)
	return fmt.Sprintf(`{"payload":[%s]}`, kiroActivitySequencedEvent(seq, event))
}

func kiroActivitySequencedEvent(seq int64, event string) string {
	return fmt.Sprintf(`{"seq":%d,%s}`, seq, strings.TrimSuffix(strings.TrimPrefix(event, "{"), "}"))
}

func TestParseKiroActivityRoutingCases(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		want        kiroActivityRoutingMeta
		wantErr     error
		wantAnyErr  bool
		wantNoState bool
	}{
		{
			name: "contract sample",
			body: kiroActivityRoutingPayload("sess_086654df", "5caac6bf-84a0", "wf_05f0cdd8",
				"investigate-architecture-overview", "investigate", `["wf_05f0cdd8","investigate"]`, "wf-planner"),
			want: kiroActivityRoutingMeta{
				SessionID: "sess_086654df", ExecutionID: "5caac6bf-84a0", WorkflowID: "wf_05f0cdd8",
				WorkflowName: "investigate-architecture-overview", NodeID: "investigate",
				NodePath: "investigate", AgentMode: "wf-planner",
			},
		},
		{
			name: "node path segments are encodeURIComponent encoded and joined",
			body: kiroActivityRoutingPayload("sess_path", "exec-path", "wf_x", "n", "gate",
				`["wf_x","steps/investi gate","中文"]`, "vibe"),
			want: kiroActivityRoutingMeta{
				SessionID: "sess_path", ExecutionID: "exec-path", WorkflowID: "wf_x", WorkflowName: "n",
				NodeID: "gate", NodePath: "steps%2Finvesti%20gate/%E4%B8%AD%E6%96%87", AgentMode: "vibe",
			},
		},
		{
			name: "node path unreserved characters stay intact",
			body: kiroActivityRoutingPayload("sess_unres", "exec-unres", "wf_u", "n", "leaf",
				`["wf_u","a b!~*'()-_.()"]`, "vibe"),
			want: kiroActivityRoutingMeta{
				SessionID: "sess_unres", ExecutionID: "exec-unres", WorkflowID: "wf_u", WorkflowName: "n",
				NodeID: "leaf", NodePath: "a%20b!~*'()-_.()", AgentMode: "vibe",
			},
		},
		{
			name: "node path preserves non-workflow first segment",
			body: kiroActivityRoutingPayload("sess_mismatch", "exec-mismatch", "wf_real", "n", "leaf",
				`["other","leaf"]`, "vibe"),
			want: kiroActivityRoutingMeta{
				SessionID: "sess_mismatch", ExecutionID: "exec-mismatch", WorkflowID: "wf_real",
				WorkflowName: "n", NodeID: "leaf", NodePath: "other/leaf", AgentMode: "vibe",
			},
		},
		{
			name: "node path cannot be empty after workflow frame",
			body: kiroActivityRoutingPayload("sess_short", "exec-short", "wf_s", "n", "leaf",
				`["wf_s"]`, "vibe"),
			wantNoState: true,
		},
		{
			name:        "workflow metadata missing",
			body:        `{"sessionId":"sess_bare","content":{"executionId":"exec-bare","message":"hello"}}`,
			wantNoState: true,
		},
		{
			name:        "duplicate whitelist keys invalidate event",
			body:        `{"sessionId":"sess_first","sessionId":"sess_second","content":{"executionId":"e1","executionId":"e2"}}`,
			wantNoState: true,
		},
		{
			name:        "oversized number token does not break parsing",
			body:        `{"sessionId":"sess_num","content":{"executionId":"exec-num","score":1e999}}`,
			wantNoState: true,
		},
		{
			name:        "subagent session id is not sess_ shaped",
			body:        kiroActivityRoutingPayload("parent", "exec-parent", "wf_p", "n", "leaf", `["wf_p","leaf"]`, "vibe"),
			wantNoState: true,
		},
		{
			name: "missing execution id is still a routable observation",
			body: `{"sessionId":"sess_noexec","seq":1,"content":{"_meta":{"kiro":{"agentMode":"vibe","workflow":{"workflowId":"wf_noexec","nodeId":"plan","nodePath":["wf_noexec","plan"]}}}}}`,
			want: kiroActivityRoutingMeta{SessionID: "sess_noexec", WorkflowID: "wf_noexec", NodeID: "plan", NodePath: "plan", AgentMode: "vibe", Sequence: 1, HasSequence: true},
		},
		{
			name:        "session id wrong type",
			body:        `{"sessionId":42,"content":{"executionId":"e1"}}`,
			wantNoState: true,
		},
		{
			name:        "null execution id",
			body:        `{"sessionId":"sess_null","content":{"executionId":null}}`,
			wantNoState: true,
		},
		{
			name:        "content array does not carry routing metadata",
			body:        `{"sessionId":"sess_arr","content":[{"executionId":"e1"}]}`,
			wantNoState: true,
		},
		{
			name:        "workflow id over limit drops the whole activity",
			body:        kiroActivityRoutingPayload("sess_long", "exec-long", strings.Repeat("w", 600), "n", "leaf", `["`+strings.Repeat("w", 600)+`","leaf"]`, "vibe"),
			wantNoState: true,
		},
		{
			name:       "malformed json",
			body:       `{"sessionId":`,
			wantAnyErr: true,
		},
		{
			name:       "truncated json",
			body:       `{"sessionId":"sess_trunc","content":{"executionId":"e1"`,
			wantAnyErr: true,
		},
		{
			name:       "top level string",
			body:       `"just a string"`,
			wantAnyErr: true,
		},
		{
			name:       "string token over 1 MiB aborts parsing",
			body:       fmt.Sprintf(`{"sessionId":"sess_big","content":{"executionId":"%s"}}`, strings.Repeat("a", kiroActivityMaxStringTokenBytes+1)),
			wantAnyErr: true,
		},
		{
			name:    "trailing json document is rejected",
			body:    `{"sessionId":"sess_tail","content":{"executionId":"e1"}}{"sessionId":"sess_tail2"}`,
			wantErr: errKiroActivityTrailingData,
		},
		{
			name:        "duplicate nodePath key invalidates event",
			body:        `{"sessionId":"sess_dup","content":{"executionId":"e1","_meta":{"kiro":{"agentMode":"vibe","workflow":{"workflowId":"wf_dup","nodeId":"gate","nodePath":["wf_dup","stale"],"nodePath":["wf_dup","gate"]}}}}}`,
			wantNoState: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, err := parseKiroActivityRouting(strings.NewReader(tc.body))
			if tc.wantAnyErr {
				if err == nil {
					t.Fatalf("parse error = nil, want failure for body %s", tc.body)
				}
				return
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("parse error = %v, want %v", err, tc.wantErr)
				}
				if len(report.Entries) != 0 {
					t.Fatalf("fatal parse retained entries: %+v", report.Entries)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse error = %v, want nil", err)
			}
			if tc.wantNoState {
				if len(report.Entries) != 0 {
					t.Fatalf("entries = %+v, want none", report.Entries)
				}
				return
			}
			if len(report.Entries) != 1 {
				t.Fatalf("entries = %+v, want one", report.Entries)
			}
			meta := report.Entries[0]
			if meta.UpdatedAt != (time.Time{}) {
				t.Fatalf("parse result must not stamp UpdatedAt, got %v", meta.UpdatedAt)
			}
			if meta != tc.want {
				t.Fatalf("meta = %+v, want %+v", meta, tc.want)
			}
		})
	}
}

func TestParseKiroActivityBatchSequenceAndSubstreams(t *testing.T) {
	event := func(seq int64, node string) string {
		return kiroActivitySequencedEvent(seq, kiroActivityRoutingPayload(
			"sess_batch", "exec", "wf_batch", "workflow", node, fmt.Sprintf(`[%q,%q]`, "wf_batch", node), "vibe"))
	}

	body := `{"payload":[` + event(2, "second") + `,` + event(1, "stale") + `,` + event(3, "third") + `]}`
	report, err := parseKiroActivityRouting(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse batch: %v", err)
	}
	if len(report.Entries) != 1 || report.Entries[0].Sequence != 3 || report.Entries[0].NodeID != "third" {
		t.Fatalf("batch result = %+v, want newest seq 3 observation", report.Entries)
	}

	substream := `{"seq":99,"sessionId":"sess_batch","subSession":"child","content":{"_meta":{"kiro":{"workflow":{"workflowId":"wf_batch","nodeId":"child","nodePath":["wf_batch","child"]}}}}}`
	body = `{"payload":[` + substream + `,` + event(4, "main") + `]}`
	report, err = parseKiroActivityRouting(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse substream batch: %v", err)
	}
	if len(report.Entries) != 1 || report.Entries[0].Sequence != 4 || report.Entries[0].NodeID != "main" {
		t.Fatalf("substream result = %+v, want main stream seq 4", report.Entries)
	}

	body = `{"payload":[` + event(5, "first") + `,` + event(5, "conflict") + `]}`
	report, err = parseKiroActivityRouting(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse conflicting replay batch: %v", err)
	}
	if len(report.Entries) != 0 || !containsKiroActivityCategory(report.Categories, kiroActivityCategorySequence) {
		t.Fatalf("conflicting replay result = %+v, want no route and sequence category", report)
	}

	unsequenced := kiroActivityRoutingPayload("sess_batch", "exec", "wf_batch", "workflow", "unsequenced", `["wf_batch","unsequenced"]`, "vibe")
	missingSeq := `{"payload":[{` + strings.TrimSuffix(strings.TrimPrefix(unsequenced, "{"), "}") + `}]}`
	report, err = parseKiroActivityRouting(strings.NewReader(missingSeq))
	if err != nil {
		t.Fatalf("parse missing-sequence event: %v body=%s", err, missingSeq)
	}
	if len(report.Entries) != 0 || !containsKiroActivityCategory(report.Categories, kiroActivityCategorySequence) {
		t.Fatalf("missing sequence result = %+v, want no route and sequence category", report)
	}
}

func containsKiroActivityCategory(categories []kiroActivityParseCategory, want kiroActivityParseCategory) bool {
	for _, category := range categories {
		if category == want {
			return true
		}
	}
	return false
}

func TestKiroActivityEncodeURIComponent(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"abcXYZ0189-_.!~*'()", "abcXYZ0189-_.!~*'()"},
		{" ", "%20"},
		{"/", "%2F"},
		{"%", "%25"},
		{"+", "%2B"},
		{"a/b c", "a%2Fb%20c"},
		{"中文", "%E4%B8%AD%E6%96%87"},
		{"\U0001F600", "%F0%9F%98%80"},
		{"\n", "%0A"},
		{string([]byte{0xff, 0xfe}), "%EF%BF%BD%EF%BF%BD"},
	}
	for _, tc := range cases {
		if got := kiroActivityEncodeURIComponent(tc.input); got != tc.want {
			t.Fatalf("kiroActivityEncodeURIComponent(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func kiroActivitySeedEntry(t *testing.T, s *Server, sessionID string, updatedAt time.Time) {
	t.Helper()
	state := s.activityRoutingStore()
	state.mu.Lock()
	state.sessions[sessionID] = kiroActivityRoutingMeta{SessionID: sessionID, ExecutionID: "exec-" + sessionID, UpdatedAt: updatedAt}
	state.mu.Unlock()
}

func TestKiroActivityRoutingStoreLifecycle(t *testing.T) {
	s := &Server{}

	// 新 seq 更新；重复、乱序和无 seq 的单例观察都不能覆盖有序状态。
	if !s.recordKiroActivityRouting(kiroActivityRoutingMeta{SessionID: "sess_a", Sequence: 2, HasSequence: true, NodeID: "plan"}) {
		t.Fatal("initial sequence observation was rejected")
	}
	if !s.recordKiroActivityRouting(kiroActivityRoutingMeta{SessionID: "sess_a", Sequence: 3, HasSequence: true, NodeID: "finalize"}) {
		t.Fatal("newer sequence observation was rejected")
	}
	if s.recordKiroActivityRouting(kiroActivityRoutingMeta{SessionID: "sess_a", Sequence: 3, HasSequence: true, NodeID: "replay"}) ||
		s.recordKiroActivityRouting(kiroActivityRoutingMeta{SessionID: "sess_a", Sequence: 1, HasSequence: true, NodeID: "old"}) ||
		s.recordKiroActivityRouting(kiroActivityRoutingMeta{SessionID: "sess_a", NodeID: "unsequenced"}) {
		t.Fatal("replayed, out-of-order, or unsequenced observation overwrote state")
	}
	meta, ok := s.lookupKiroActivityRouting("sess_a")
	if !ok || meta.NodeID != "finalize" || meta.Sequence != 3 || meta.UpdatedAt.IsZero() {
		t.Fatalf("lookup after overwrite = %+v ok=%v, want exec-new/finalize with UpdatedAt", meta, ok)
	}

	// Server 生命周期彼此隔离。
	other := &Server{}
	if _, ok := other.lookupKiroActivityRouting("sess_a"); ok {
		t.Fatal("routing observations leaked across Server instances")
	}

	// 空闲超过 24h：lookup 惰性过期。
	kiroActivitySeedEntry(t, s, "sess_expired", time.Now().Add(-kiroActivityRoutingIdleTTL-time.Minute))
	if _, ok := s.lookupKiroActivityRouting("sess_expired"); ok {
		t.Fatal("expired entry still routable")
	}
	state := s.activityRoutingStore()
	state.mu.Lock()
	_, exists := state.sessions["sess_expired"]
	state.mu.Unlock()
	if exists {
		t.Fatal("expired entry was not removed by lookup")
	}

	// record 写入时惰性清理过期条目，且不影响新鲜条目。
	kiroActivitySeedEntry(t, s, "sess_stale", time.Now().Add(-25*time.Hour))
	s.recordKiroActivityRouting(kiroActivityRoutingMeta{SessionID: "sess_fresh", NodeID: "plan"})
	state.mu.Lock()
	_, staleExists := state.sessions["sess_stale"]
	freshExists := state.sessions["sess_fresh"].NodeID == "plan"
	state.mu.Unlock()
	if staleExists || !freshExists {
		t.Fatalf("record cleanup wrong: staleExists=%v freshExists=%v", staleExists, freshExists)
	}

	// 容量上限 512：满员时淘汰最久未更新条目。
	s = &Server{}
	base := time.Now().Add(-time.Hour)
	state = s.activityRoutingStore()
	state.mu.Lock()
	for i := 0; i < kiroActivityRoutingMaxSessions; i++ {
		id := fmt.Sprintf("sess_cap-%03d", i)
		state.sessions[id] = kiroActivityRoutingMeta{SessionID: id, UpdatedAt: base.Add(time.Duration(i) * time.Second)}
	}
	state.mu.Unlock()
	s.recordKiroActivityRouting(kiroActivityRoutingMeta{SessionID: "sess_cap-new", NodeID: "plan"})
	state.mu.Lock()
	size := len(state.sessions)
	_, oldestExists := state.sessions["sess_cap-000"]
	_, newestExists := state.sessions["sess_cap-511"]
	_, insertedExists := state.sessions["sess_cap-new"]
	state.mu.Unlock()
	if size != kiroActivityRoutingMaxSessions || oldestExists || !newestExists || !insertedExists {
		t.Fatalf("capacity eviction wrong: size=%d oldestExists=%v newestExists=%v insertedExists=%v",
			size, oldestExists, newestExists, insertedExists)
	}

	// 空 SessionID 不入存储。
	s.recordKiroActivityRouting(kiroActivityRoutingMeta{ExecutionID: "orphan"})
	state.mu.Lock()
	size = len(state.sessions)
	state.mu.Unlock()
	if size != kiroActivityRoutingMaxSessions {
		t.Fatalf("empty session id must not be stored, size=%d", size)
	}
}

func TestKiroActivityRoutingStoreConcurrentAccess(t *testing.T) {
	s := &Server{}
	const writers = 8
	const perWriter = 200
	const keys = 16
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				id := fmt.Sprintf("sess_race-%d", i%keys)
				sequence := int64(w*perWriter + i + 1)
				s.recordKiroActivityRouting(kiroActivityRoutingMeta{SessionID: id, Sequence: sequence, HasSequence: true, NodeID: "plan"})
				// 命中与否取决于调度时序，并发访问本身由 -race 验证。
				_, _ = s.lookupKiroActivityRouting(fmt.Sprintf("sess_race-%d", (i+1)%keys))
				_, _ = s.lookupKiroActivityRouting(id)
			}
		}(w)
	}
	wg.Wait()
	for k := 0; k < keys; k++ {
		id := fmt.Sprintf("sess_race-%d", k)
		meta, ok := s.lookupKiroActivityRouting(id)
		if !ok || !meta.HasSequence || meta.Sequence <= 0 || meta.NodeID != "plan" {
			t.Fatalf("final state for %s = %+v ok=%v, want a sequenced observation", id, meta, ok)
		}
	}
}

func TestKiroActivityDiscardRecordsRoutingState(t *testing.T) {
	s := &Server{}

	req := httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity", strings.NewReader(
		kiroActivityRoutingPayload("sess_discard", "exec-discard", "wf_d", "n", "plan", `["wf_d","plan"]`, "wf-planner")))
	report, parseErr, discardErr := s.discardKiroActivityRequestBody(req)
	if parseErr != nil || discardErr != nil {
		t.Fatalf("discard errors: parse=%v discard=%v", parseErr, discardErr)
	}
	stored, ok := s.lookupKiroActivityRouting("sess_discard")
	if !ok || stored.ExecutionID != "exec-discard" || stored.NodePath != "plan" || stored.AgentMode != "wf-planner" {
		t.Fatalf("stored meta = %+v ok=%v, want recorded routing state", stored, ok)
	}
	if len(report.Entries) != 1 || report.Entries[0].SessionID != "sess_discard" {
		t.Fatalf("returned report = %+v", report)
	}

	// 无可建立状态的 activity：照常消费，不入存储。
	req = httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity", strings.NewReader(`{"sessionId":"parent","keepAlive":true}`))
	report, parseErr, discardErr = s.discardKiroActivityRequestBody(req)
	if parseErr != nil || discardErr != nil || len(report.Entries) != 0 {
		t.Fatalf("unroutable discard: parse=%v discard=%v", parseErr, discardErr)
	}
	if _, ok := s.lookupKiroActivityRouting("parent"); ok {
		t.Fatal("subagent session must not create routing state")
	}

	// 读失败：返回错误且不建立状态。
	req = httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity", nil)
	req.Body = &kiroActivityFailingBody{readErr: errors.New("synthetic read failure")}
	_, parseErr, discardErr = s.discardKiroActivityRequestBody(req)
	if parseErr == nil || discardErr == nil {
		t.Fatalf("read failure must surface errors, parse=%v discard=%v", parseErr, discardErr)
	}
	if _, ok := s.lookupKiroActivityRouting("sess_partial"); ok {
		t.Fatal("failed read must not create routing state")
	}

	// Close failure also prevents committing an otherwise valid parsed report.
	validBody := strings.NewReader(kiroActivityRoutingPayload("sess_close_fail", "exec", "wf_close", "n", "plan", `["wf_close","plan"]`, "vibe"))
	closeFailBody := &kiroActivityReadCloser{Reader: validBody, closeErr: errors.New("synthetic close failure")}
	req = httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity", nil)
	req.Body = closeFailBody
	_, parseErr, discardErr = s.discardKiroActivityRequestBody(req)
	if parseErr != nil || discardErr == nil {
		t.Fatalf("close failure errors: parse=%v discard=%v", parseErr, discardErr)
	}
	if _, ok := s.lookupKiroActivityRouting("sess_close_fail"); ok {
		t.Fatal("close failure must not commit parsed state")
	}
}

type kiroActivityReadCloser struct {
	io.Reader
	closeErr error
}

func (b *kiroActivityReadCloser) Close() error { return b.closeErr }

func TestKiroActivitySynthesizedHeaderPassesApplyWorkflowStageHeader(t *testing.T) {
	convID := "sess_hdr-check"
	rawBody := fmt.Sprintf(`{"model":"auto","stream":true,"input":[{"role":"user","content":"task"}],"metadata":{"kiro:conversation_id":%q,"kiro:agent_mode":"vibe","kiro:proxy_continuation_id":"cont-1","kiro:proxy_current_tool_results":0}}`, convID)
	conv, err := converter.ParseKiroRawResponsesRouting([]byte(rawBody))
	if err != nil {
		t.Fatal(err)
	}
	meta := kiroActivityRoutingMeta{
		SessionID: convID, WorkflowID: "wf_node01", WorkflowName: "investigate", NodeID: "plan",
		NodePath: "plan", AgentMode: "wf-planner", ExecutionID: "exec-hdr",
	}
	header, err := buildKiroActivityWorkflowHeader(conv, meta)
	if err != nil {
		t.Fatalf("build synthesized header: %v", err)
	}
	if err := converter.ApplyWorkflowStageHeader(conv, header); err != nil {
		t.Fatalf("synthesized header rejected by ApplyWorkflowStageHeader: %v", err)
	}
	if conv.WorkflowMetadataVersion != 2 || conv.WorkflowID != "wf_node01" || conv.WorkflowNodeID != "plan" ||
		conv.WorkflowNodePath != "plan" || conv.WorkflowExecutionID != "" {
		t.Fatalf("bound conversation = %+v", conv)
	}
	// 官方不提供的字段保持不合成：definitionId/definitionRevision/taskType 绑定为空值。
	if conv.WorkflowDefinitionID != "" || conv.WorkflowDefinitionRevision != "" || conv.WorkflowTaskType != "" {
		t.Fatalf("unprovided v2 identity fields must stay empty: %+v", conv)
	}
	if !conv.WorkflowStageProvided || conv.WorkflowStageSource != "request_header" {
		t.Fatalf("stage provenance wrong: provided=%v source=%q", conv.WorkflowStageProvided, conv.WorkflowStageSource)
	}

	// 无法通过既有路由身份校验的 activity 字段：合成失败并回退，不改契约。
	badMeta := meta
	badMeta.NodeID = " padded"
	if _, err := buildKiroActivityWorkflowHeader(conv, badMeta); err == nil {
		t.Fatal("padded node id must fail synthesis")
	}
	badMeta = meta
	badMeta.NodePath = strings.Repeat("p", kiroActivityNodePathMaxBytes+1)
	if _, err := buildKiroActivityWorkflowHeader(conv, badMeta); err == nil {
		t.Fatal("overlong node path must fail synthesis")
	}
}

func TestKiroActivityWorkflowHeaderProvenanceAndAgentModeCompatibility(t *testing.T) {
	const conversation = "sess_provenance"
	requestBody := `{"model":"auto","stream":true,"input":[{"role":"user","content":"task"}],"metadata":{"kiro:conversation_id":"sess_provenance","kiro:agent_mode":"vibe","kiro:agent_continuation_id":"cont-1"}}`
	newRequest := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/v1/responses", strings.NewReader(requestBody))
	}

	s, _, _ := newKiroActivityTestServer(t, ModeForward, "")
	s.recordKiroActivityRouting(kiroActivityRoutingMeta{
		SessionID: conversation, Sequence: 1, HasSequence: true,
		WorkflowID: "wf_provenance", NodeID: "plan", NodePath: "plan", AgentMode: "vibe",
	})
	req, ok := s.synthesizeKiroActivityWorkflowHeader(newRequest(), []byte(requestBody))
	if !ok {
		t.Fatal("compatible activity observation did not synthesize a workflow header")
	}
	if !isKiroActivityWorkflowRequest(req) {
		t.Fatal("synthetic activity workflow request lost provenance context")
	}
	headers := workflowStageHeaders(req)
	if len(headers) != 1 {
		t.Fatalf("synthesized headers = %v, want one", headers)
	}
	var header kiroActivitySynthesizedHeader
	if err := json.Unmarshal([]byte(headers[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header.ExecutionID != "" || header.NodePath != "plan" || header.AgentMode != "vibe" || header.AgentContinuationID != "cont-1" {
		t.Fatalf("synthesized header = %+v, want request-bound metadata and empty execution id", header)
	}

	other, _, _ := newKiroActivityTestServer(t, ModeForward, "")
	other.recordKiroActivityRouting(kiroActivityRoutingMeta{
		SessionID: conversation, Sequence: 1, HasSequence: true,
		WorkflowID: "wf_provenance", NodeID: "plan", NodePath: "plan", AgentMode: "wf-planner",
	})
	unchanged, ok := other.synthesizeKiroActivityWorkflowHeader(newRequest(), []byte(requestBody))
	if ok || isKiroActivityWorkflowRequest(unchanged) {
		t.Fatal("incompatible agent mode borrowed another activity observation")
	}
}

type kiroActivityCountingBody struct {
	reader io.Reader
	read   int64
}

func (b *kiroActivityCountingBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += int64(n)
	return n, err
}
func (b *kiroActivityCountingBody) Close() error { return nil }

type kiroActivityPatternReader struct {
	pattern   byte
	remaining int64
}

func (r *kiroActivityPatternReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	for i := 0; i < n; i++ {
		p[i] = r.pattern
	}
	r.remaining -= int64(n)
	return n, nil
}

func TestKiroActivityDrainsOversizedTokenJSONAsStream(t *testing.T) {
	s, _, _ := newKiroActivityTestServer(t, ModeForward, "")
	prefix := `{"sessionId":"sess_big","padding":"noise","content":{"executionId":"`
	suffix := `"}}`
	body := &kiroActivityCountingBody{reader: io.MultiReader(
		strings.NewReader(prefix),
		&kiroActivityPatternReader{pattern: 'a', remaining: 40 << 20},
		strings.NewReader(suffix),
	)}
	req := httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity", nil)
	req.Body = body
	rr := httptest.NewRecorder()
	s.proxy.ServeHTTP(rr, req)
	wantBytes := int64(len(prefix)) + 40<<20 + int64(len(suffix))
	if rr.Code != http.StatusNoContent || body.read != wantBytes {
		t.Fatalf("response=%d bytesRead=%d, want 204 and %d bytes consumed", rr.Code, body.read, wantBytes)
	}
	if _, ok := s.lookupKiroActivityRouting("sess_big"); ok {
		t.Fatal("oversized token activity must not create routing state")
	}
}

func kiroActivityRoutingRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity?source=test", strings.NewReader(body))
}

func kiroActivityNativeContinuationRequest(t *testing.T, conversation, continuation string) *http.Request {
	t.Helper()
	req := rawStageRequest(t, "auto", conversation, "", 0)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = req.Body.Close()
	needle := `,"kiro:proxy_continuation_id":""`
	replacement := fmt.Sprintf(`,"kiro:agent_continuation_id":%q,"kiro:proxy_continuation_id":""`, continuation)
	updated := strings.Replace(string(body), needle, replacement, 1)
	if updated == string(body) {
		t.Fatal("raw request fixture is missing legacy continuation metadata")
	}
	req.Body = io.NopCloser(strings.NewReader(updated))
	req.ContentLength = int64(len(updated))
	return req
}

func TestKiroActivityRoutingDrivesHeaderlessRawAuto(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)

	var passthroughMu sync.Mutex
	var passthroughHits int
	var passthroughBodies []string
	rawStageProxy(t, env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		passthroughMu.Lock()
		passthroughHits++
		passthroughBodies = append(passthroughBodies, string(body))
		passthroughMu.Unlock()
		_, _ = io.WriteString(w, "passthrough")
	}))

	conv := "sess_activity-routing-1"
	executionID := "5caac6bf-84a0-4460-ac1c-2b201dd29e40"

	// 1. activity 建立路由状态（plan 节点）。
	resp := serveRawStage(t, env, kiroActivityRoutingRequest(t, kiroActivityBatchRoutingPayload(1,
		conv, executionID, "wf_node01", "investigate-architecture-overview", "plan", `["wf_node01","plan"]`, "vibe")))
	if resp.Code != http.StatusNoContent || resp.Body.Len() != 0 {
		t.Fatalf("activity response = %d %q, want empty 204", resp.Code, resp.Body.String())
	}
	if _, ok := env.server.lookupKiroActivityRouting(conv); !ok {
		t.Fatal("activity did not establish routing state")
	}

	// 2. 同会话无头 raw auto 请求命中本地路由：nodeId=plan → shared-responses。
	resp = serveRawStage(t, env, kiroActivityNativeContinuationRequest(t, conv, "continuation-1"))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "response.completed") {
		t.Fatalf("headerless auto response = %d %s", resp.Code, resp.Body.String())
	}
	observed := <-env.requests
	if observed.Path != "/v1/responses" || observed.Model != "upstream-shared-responses" {
		t.Fatalf("routed upstream = %+v, want upstream-shared-responses via /v1/responses", observed)
	}

	// 3. 同一原生 continuation 保持首请求选定的模型，即使后续 activity 观察到 finalize。
	resp = serveRawStage(t, env, kiroActivityRoutingRequest(t, kiroActivityBatchRoutingPayload(2,
		conv, executionID, "wf_node01", "investigate-architecture-overview", "finalize", `["wf_node01","finalize"]`, "vibe")))
	if resp.Code != http.StatusNoContent {
		t.Fatalf("activity update response = %d, want 204", resp.Code)
	}
	resp = serveRawStage(t, env, kiroActivityNativeContinuationRequest(t, conv, "continuation-1"))
	if resp.Code != http.StatusOK {
		t.Fatalf("locked auto response = %d %s", resp.Code, resp.Body.String())
	}
	observed = <-env.requests
	if observed.Model != "upstream-shared-responses" {
		t.Fatalf("continuation lock bypassed: routed = %+v, want locked upstream-shared-responses", observed)
	}

	// 新 continuation 可以根据当时已有的 finalize 观察重新选模。
	resp = serveRawStage(t, env, kiroActivityNativeContinuationRequest(t, conv, "continuation-2"))
	if resp.Code != http.StatusOK {
		t.Fatalf("new continuation auto response = %d %s", resp.Code, resp.Body.String())
	}
	observed = <-env.requests
	if observed.Model != "upstream-finalize" {
		t.Fatalf("new continuation routed = %+v, want latest finalize observation", observed)
	}

	// 5. 无状态会话按 v0 元数据落 default 本地兜底，不放行官方。
	untracked := "sess_untracked"
	resp = serveRawStage(t, env, rawStageRequest(t, "auto", untracked, "", 0))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "response.completed") {
		t.Fatalf("untracked auto response = %d %q, want local default routing", resp.Code, resp.Body.String())
	}
	observed = <-env.requests
	if observed.Model != "upstream-default" {
		t.Fatalf("untracked session routed = %+v, want upstream-default", observed)
	}
	passthroughMu.Lock()
	hits, bodies := passthroughHits, append([]string(nil), passthroughBodies...)
	passthroughMu.Unlock()
	if hits != 0 || len(bodies) != 0 {
		t.Fatalf("passthrough observations = %d %q, want none", hits, bodies)
	}
}

func TestKiroActivityRoutingUnroutableFallsBackToLocalDefault(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)

	var passthroughHits atomic.Int64
	rawStageProxy(t, env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passthroughHits.Add(1)
		_, _ = io.WriteString(w, "passthrough")
	}))

	cases := []struct {
		name string
		body string
	}{
		{"bad json", `{"sessionId":"sess_bad",`},
		{"subagent session", kiroActivityRoutingPayload("parent", "exec-p", "wf_p", "n", "plan", `["wf_p","plan"]`, "vibe")},
		{"missing execution", `{"sessionId":"sess_noexec","content":{"_meta":{"kiro":{"agentMode":"vibe"}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := serveRawStage(t, env, kiroActivityRoutingRequest(t, tc.body))
			if resp.Code != http.StatusNoContent || resp.Body.Len() != 0 {
				t.Fatalf("%s: activity response = %d %q, want empty 204", tc.name, resp.Code, resp.Body.String())
			}
		})
	}

	// 无头 raw auto 在状态未命中时按 v0 元数据落 default 模型兜底；
	// 官方会拒绝 passthrough 的 auto 请求，放行路径不得再被使用。
	conv := "sess_bad"
	resp := serveRawStage(t, env, rawStageRequest(t, "auto", conv, "", 0))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "response.completed") {
		t.Fatalf("auto after unroutable activity = %d %q, want local default routing", resp.Code, resp.Body.String())
	}
	observed := <-env.requests
	if observed.Model != "upstream-default" {
		t.Fatalf("fallback routed = %+v, want upstream-default", observed)
	}
	if got := passthroughHits.Load(); got != 0 {
		t.Fatalf("passthrough hits = %d, want 0", got)
	}
}

func TestKiroActivityRoutingDrivesDutyNodePathSecondaryMatch(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)
	rawStageProxy(t, env, nil)

	config := store.AutoRoutingConfig{
		Version: store.AutoRoutingVersion, Mode: store.AutoRoutingModeDutyFixed,
		Layout: store.AutoRoutingLayoutStandard, Enabled: true, DefaultModelID: "default",
		DutyModels: dutyModelsForIntegrationTest(),
		NodeOverrides: []store.AutoNodeOverride{{
			DefinitionID: "content://legacy-definition", DefinitionRevision: "1",
			NodePath: "investigate", ModelID: "implement", ReasoningEffort: "high",
		}},
	}
	setAutoStageIntegrationConfig(t, env.db, config)
	env.server.router.Reload()

	// 1. activity 建立会话状态（exec1，investigate 节点）。
	resp := serveRawStage(t, env, kiroActivityRoutingRequest(t, kiroActivityBatchRoutingPayload(1,
		"sess_duty-path-1", "exec-duty-1", "wf_duty01", "investigate-coverage", "investigate", `["wf_duty01","investigate"]`, "vibe")))
	if resp.Code != http.StatusNoContent || resp.Body.Len() != 0 {
		t.Fatalf("activity response = %d %q, want empty 204", resp.Code, resp.Body.String())
	}

	// 2. 无头 auto 请求：合成元数据无 definitionId，经 nodePath 二级匹配命中 override。
	resp = serveRawStage(t, env, kiroActivityNativeContinuationRequest(t, "sess_duty-path-1", "duty-continuation-1"))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "response.completed") {
		t.Fatalf("secondary match auto response = %d %s", resp.Code, resp.Body.String())
	}
	observed := <-env.requests
	if observed.Model != "upstream-implement" {
		t.Fatalf("routed upstream = %+v, want upstream-implement via nodePath secondary match", observed)
	}

	// 3. 同 continuation 的模型保持锁定；后续 activity 不能换选。
	resp = serveRawStage(t, env, kiroActivityRoutingRequest(t, kiroActivityBatchRoutingPayload(2,
		"sess_duty-path-1", "exec-duty-1", "wf_duty01", "investigate-coverage", "plan", `["wf_duty01","plan"]`, "vibe")))
	if resp.Code != http.StatusNoContent {
		t.Fatalf("activity update response = %d, want 204", resp.Code)
	}
	resp = serveRawStage(t, env, kiroActivityNativeContinuationRequest(t, "sess_duty-path-1", "duty-continuation-1"))
	if resp.Code != http.StatusOK {
		t.Fatalf("locked auto response = %d %s", resp.Code, resp.Body.String())
	}
	observed = <-env.requests
	if observed.Model != "upstream-implement" {
		t.Fatalf("continuation lock bypassed: routed = %+v, want locked upstream-implement", observed)
	}

	// 4. 新 continuation 落在无配置节点：二级不命中，落 default。
	resp = serveRawStage(t, env, kiroActivityRoutingRequest(t, kiroActivityBatchRoutingPayload(3,
		"sess_duty-path-1", "exec-duty-2", "wf_duty01", "investigate-coverage", "review", `["wf_duty01","review"]`, "vibe")))
	if resp.Code != http.StatusNoContent {
		t.Fatalf("activity exec2 response = %d, want 204", resp.Code)
	}
	resp = serveRawStage(t, env, kiroActivityNativeContinuationRequest(t, "sess_duty-path-1", "duty-continuation-2"))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "response.completed") {
		t.Fatalf("unmatched auto response = %d %s", resp.Code, resp.Body.String())
	}
	observed = <-env.requests
	if observed.Model != "upstream-default" {
		t.Fatalf("unmatched node routed = %+v, want upstream-default", observed)
	}
}

func dutyModelsForIntegrationTest() map[string]store.AutoDutyTarget {
	targets := make(map[string]store.AutoDutyTarget, len(store.AutoDutyNames(store.AutoRoutingLayoutStandard)))
	for _, duty := range store.AutoDutyNames(store.AutoRoutingLayoutStandard) {
		targets[duty] = store.AutoDutyTarget{ModelID: "run"}
	}
	return targets
}
