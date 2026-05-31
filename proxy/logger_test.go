package proxy

import (
	"strings"
	"testing"
)

func TestTruncateLogDetail(t *testing.T) {
	short := strings.Repeat("a", 1024)
	if got := truncateLogDetail(short, defaultMaxLogDetailBytes); got != short {
		t.Fatal("short detail should not be changed")
	}

	long := strings.Repeat("b", defaultMaxLogDetailBytes+10)
	got := truncateLogDetail(long, defaultMaxLogDetailBytes)
	if len(got) <= defaultMaxLogDetailBytes || len(got) <= len(long) {
		t.Fatalf("unexpected truncated length: got=%d original=%d", len(got), len(long))
	}
	if !strings.Contains(got, "truncated log detail") {
		t.Fatalf("missing truncation marker: %q", got[len(got)-80:])
	}
}

func TestTrafficLoggerDetailModes(t *testing.T) {
	body := []byte(strings.Repeat("x", 100))
	summary, err := NewTrafficLogger("", nil, LogOptions{DetailMode: "summary", MaxDetailBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	gotSummary := summary.prepareDetailBytes(body)
	if !strings.HasPrefix(gotSummary, strings.Repeat("x", 10)) {
		t.Fatalf("summary mode should keep first 10 bytes, got %q", gotSummary)
	}
	if !strings.Contains(gotSummary, "truncated log detail") {
		t.Fatalf("summary mode should include truncation marker, got %q", gotSummary)
	}
	if gotSummary == string(body) {
		t.Fatal("summary mode should not preserve full detail when over limit")
	}

	full, err := NewTrafficLogger("", nil, LogOptions{DetailMode: "full", MaxDetailBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	gotFull := full.prepareDetailBytes(body)
	if gotFull != string(body) {
		t.Fatal("full mode should preserve full detail")
	}
}
