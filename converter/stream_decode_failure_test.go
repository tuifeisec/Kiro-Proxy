package converter

import (
	"io"
	"strings"
	"testing"
)

func TestStreamEventSampleRedactsAndTruncates(t *testing.T) {
	data := `{"error":"bad","headers":{"Authorization":"Bearer sk-proj-abcdefghijklmnop"},"api_key":"plain-secret-value","note":"key sk-live1234567890abcd"}` + strings.Repeat("长", 200)
	message := streamDecodeFailureMessage("Chat", data, io.ErrUnexpectedEOF)
	for _, secret := range []string{"sk-proj-abcdefghijklmnop", "plain-secret-value", "sk-live1234567890abcd"} {
		if strings.Contains(message, secret) {
			t.Fatalf("secret %q leaked: %s", secret, message)
		}
	}
	if !strings.Contains(message, "[REDACTED]") || !strings.Contains(message, "…") {
		t.Fatalf("message not redacted/truncated: %s", message)
	}
	if sample := streamEventSample(data); len(sample) > streamEventSampleLimit+len("…") {
		t.Fatalf("sample length = %d", len(sample))
	}
}

func TestStreamFailureTextPrefersDetail(t *testing.T) {
	if got := streamFailureText(StreamOutcomeMissingTerminal, ""); got != streamFailureMessage(StreamOutcomeMissingTerminal) {
		t.Fatalf("default text = %q", got)
	}
	if got := streamOutcomeErrorWithDetail(StreamOutcomeUpstreamProtocolFailure, "具体原因"); got == nil || !strings.Contains(got.Error(), "具体原因") {
		t.Fatalf("error with detail = %v", got)
	}
	if got := streamOutcomeErrorWithDetail(StreamOutcomeCompleted, "ignored"); got != nil {
		t.Fatalf("completed stream returned error %v", got)
	}
}
