package proxy

import (
	"bufio"
	"os"
	"path/filepath"
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

func TestTrafficLoggerFullDetailHasHardLimit(t *testing.T) {
	logger, err := NewTrafficLogger("", nil, LogOptions{DetailMode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	detail := []byte(strings.Repeat("x", maxFullLogDetailBytes+1))
	got := logger.prepareDetailBytes(detail)
	if !strings.Contains(got, "truncated log detail") {
		t.Fatal("full detail above the hard limit was not truncated")
	}
	if !strings.HasPrefix(got, strings.Repeat("x", 128)) {
		t.Fatal("truncated detail did not preserve its prefix")
	}
}

func TestQueryLogEntriesHandlesLongLinesAndKeepsNewest(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "traffic.log")
	longDetail := strings.Repeat("x", 1024*1024)
	content := strings.Join([]string{
		`{"id":"one","date":"2026-08-11"}`,
		`{"id":"two","date":"2026-08-11","detail":"` + longDetail + `"}`,
		`{"id":"other","date":"2026-08-10"}`,
		`{"id":"three","date":"2026-08-11"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	entries, err := queryLogEntriesByDate(logFile, "2026-08-11", 2)
	if err != nil {
		t.Fatalf("query logs: %v", err)
	}
	if len(entries) != 2 || entries[0].ID != "three" || entries[1].ID != "two" {
		t.Fatalf("unexpected newest entries: %#v", entries)
	}
}

func TestReadLogLineDrainsOversizedLine(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(strings.Repeat("x", 2048) + "\nnext\n"))
	line, tooLong, err := readLogLine(reader, 1024)
	if err != nil || !tooLong || len(line) != 0 {
		t.Fatalf("unexpected oversized line result: len=%d tooLong=%v err=%v", len(line), tooLong, err)
	}
	line, tooLong, err = readLogLine(reader, 1024)
	if err != nil || tooLong || string(line) != "next" {
		t.Fatalf("reader did not continue after oversized line: line=%q tooLong=%v err=%v", line, tooLong, err)
	}
}

func TestTrafficLoggerRotatesToSingleBackup(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "traffic.log")
	if err := os.WriteFile(logFile, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	logger, err := NewTrafficLogger(logFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger.fileSize = maxTrafficLogBytes
	logger.writeFile([]byte("new"))
	logger.Close()

	current, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(logFile + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "new\n" || string(backup) != "old\n" {
		t.Fatalf("unexpected rotation result: current=%q backup=%q", current, backup)
	}
}
