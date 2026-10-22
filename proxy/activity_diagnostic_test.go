package proxy

import (
	"strings"
	"testing"
)

func TestKiroActivityWorkflowUnavailableDiagnostic(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		want        kiroActivityParseCategory
		wantEntries int
	}{
		{
			name: "ordinary vibe activity without workflow",
			body: `{"sessionId":"sess_vibe","agentMode":"vibe","content":{"executionId":"exec-vibe"}}`,
			want: kiroActivityCategoryWorkflowUnavailable,
		},
		{
			name: "provided but incomplete workflow",
			body: `{"sessionId":"sess_partial","content":{"_meta":{"kiro":{"workflow":{"workflowId":"wf_partial"}}}}}`,
			want: kiroActivityCategoryEventUnroutable,
		},
		{
			name:        "valid workflow",
			body:        `{"sessionId":"sess_valid","content":{"_meta":{"kiro":{"workflow":{"workflowId":"wf_valid","nodeId":"plan","nodePath":["wf_valid","plan"]}}}}}`,
			wantEntries: 1,
		},
		{
			name: "invalid workflow type takes precedence over missing workflow",
			body: `{"sessionId":"sess_bad_type","content":{"_meta":{"kiro":{"workflow":"invalid"}}}}`,
			want: kiroActivityCategoryEventInvalid,
		},
		{
			name: "duplicate workflow key is invalid rather than unavailable",
			body: `{"sessionId":"sess_duplicate","content":{"_meta":{"kiro":{"workflow":{},"workflow":{}}}}}`,
			want: kiroActivityCategoryEventInvalid,
		},
		{
			name: "substream is ignored before workflow classification",
			body: `{"sessionId":"sess_child","subSession":"child","content":{"executionId":"exec-child"}}`,
			want: kiroActivityCategorySubstream,
		},
		{
			name: "invalid session keeps unroutable classification",
			body: `{"sessionId":"not-a-session","content":{"executionId":"exec-invalid-session"}}`,
			want: kiroActivityCategoryEventUnroutable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, err := parseKiroActivityRouting(strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("parse activity: %v", err)
			}
			if len(report.Entries) != tc.wantEntries {
				t.Fatalf("entries = %+v, want %d", report.Entries, tc.wantEntries)
			}
			if tc.want != "" && !containsKiroActivityCategory(report.Categories, tc.want) {
				t.Fatalf("categories = %v, want %q", report.Categories, tc.want)
			}
			if tc.want != kiroActivityCategoryWorkflowUnavailable &&
				containsKiroActivityCategory(report.Categories, kiroActivityCategoryWorkflowUnavailable) {
				t.Fatalf("categories = %v, unexpected workflow unavailable", report.Categories)
			}
		})
	}
}

func TestKiroActivityMixedBatchAndMissingWorkflowDoNotReplaceState(t *testing.T) {
	valid := `{"seq":4,"sessionId":"sess_shared","content":{"_meta":{"kiro":{"workflow":{"workflowId":"wf_old","nodeId":"plan","nodePath":["wf_old","plan"]}}}}}`
	missing := `{"seq":5,"sessionId":"sess_shared","agentMode":"vibe","content":{"executionId":"exec-no-workflow"}}`
	newValid := `{"seq":6,"sessionId":"sess_new","content":{"_meta":{"kiro":{"workflow":{"workflowId":"wf_new","nodeId":"review","nodePath":["wf_new","review"]}}}}}`
	body := `{"payload":[` + valid + `,` + missing + `,` + newValid + `]}`
	report, err := parseKiroActivityRouting(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse mixed batch: %v", err)
	}
	if !containsKiroActivityCategory(report.Categories, kiroActivityCategoryWorkflowUnavailable) {
		t.Fatalf("categories = %v, want workflow unavailable", report.Categories)
	}
	if len(report.Entries) != 2 {
		t.Fatalf("entries = %+v, want only the two workflow events", report.Entries)
	}

	server := &Server{}
	for _, entry := range report.Entries {
		server.recordKiroActivityRouting(entry)
	}
	got, ok := server.lookupKiroActivityRouting("sess_shared")
	if !ok || got.WorkflowID != "wf_old" || got.NodeID != "plan" || got.Sequence != 4 {
		t.Fatalf("state after missing-workflow event = %+v, exists=%v; want original workflow observation", got, ok)
	}
	if got, ok := server.lookupKiroActivityRouting("sess_new"); !ok || got.WorkflowID != "wf_new" || got.NodeID != "review" {
		t.Fatalf("valid batch event not committed: %+v, exists=%v", got, ok)
	}
}
