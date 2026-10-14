package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRawResponsesFailureDiagnosticPreservesProtocolAndRedactsMessages(t *testing.T) {
	for _, tc := range []struct {
		name, data, want string
		sse              bool
	}{
		{"nested metadata refusal", `data: {"type":"response.failed","response":{"status":"failed","error":{"type":"invalid_request_error","code":"unsupported_parameter","param":"metadata","message":"PRIVATE_PROMPT_AND_KEY"}}}` + "\n\n", "event=response.failed, type=invalid_request_error, code=unsupported_parameter, param=metadata", true},
		{"header error event", "event: response.error\ndata: {\"error\":{\"code\":\"server_error\",\"message\":\"PRIVATE_PROMPT_AND_KEY\"}}\n\n", "event=response.error, code=server_error", true},
		{"incomplete", `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n", "event=response.incomplete, reason=max_output_tokens", true},
		{"unknown values omitted", `data: {"type":"error","error":{"code":"PRIVATE_PROMPT_AND_KEY","type":"PRIVATE_PROMPT_AND_KEY","param":"PRIVATE_PROMPT_AND_KEY","message":"PRIVATE_PROMPT_AND_KEY"}}` + "\n\n", "event=error", true},
		{"JSON single model failure", `{"status":"failed","error":{"type":"server_error","message":"PRIVATE_PROMPT_AND_KEY"}}`, "status=failed, type=server_error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := rawResponsesTestDB(t)
			model := rawResponsesTestModel("", "openai", "/responses")
			f, stats := rawResponsesTestForwarder(t, db)
			usage := newRequestUsageRecorder(db, time.Now(), model, "/v1/responses", "kiro-runtime-responses")
			lease, ok := f.load.acquire(model.ID, 1)
			if !ok {
				t.Fatal("acquire lease")
			}
			observed := newRawResponsesUsageBody(io.NopCloser(strings.NewReader(tc.data)), usage, http.StatusOK, tc.sse)
			observed.lease, observed.stats, observed.model = lease, stats, model
			var got bytes.Buffer
			if _, err := io.CopyBuffer(&got, observed, make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			_ = observed.Close()
			if got.String() != tc.data {
				t.Fatalf("upstream response changed: %q", got.String())
			}
			rows := rawResponsesUsageItems(t, db)
			want := "raw Responses stream reported failure (" + tc.want + ")"
			if len(rows) != 1 || rows[0].Success || rows[0].ErrorMessage != want {
				t.Fatalf("usage=%+v want error=%q", rows, want)
			}
			if strings.Contains(rows[0].ErrorMessage, "PRIVATE_PROMPT_AND_KEY") || f.load.snapshot()[model.ID] != 0 || stats.Snapshot().TotalErrors != 1 {
				t.Fatal("diagnostic leaked content or finalized accounting incorrectly")
			}
		})
	}
}
