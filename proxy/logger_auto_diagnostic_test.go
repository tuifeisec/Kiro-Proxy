package proxy

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func readAutoDiagnosticEntries(t *testing.T, path string) []LogEntry {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var entries []LogEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode diagnostic entry: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestTrafficLoggerAutoDiagnosticIsIsolatedAndAppends(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "traffic.log")
	logger, err := NewTrafficLogger(logFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"schema_version": 99,
		"run_id":         "caller-value",
		"event_id":       "caller-event",
		"safe_field":     "safe-value",
	}
	if err := logger.AutoDiagnostic("route_selected", metadata); err != nil {
		t.Fatal(err)
	}
	logger.Info("request", "ordinary traffic", "sensitive-body-or-header")
	logger.Close()
	if err := logger.AutoDiagnostic("after-close", nil); err == nil {
		t.Fatal("expected AutoDiagnostic to fail after Close")
	}

	path := logFile + ".auto-diagnostic.log"
	entries := readAutoDiagnosticEntries(t, path)
	if len(entries) != 1 {
		t.Fatalf("got %d diagnostic entries, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Category != "auto-diagnostic" || entry.Message != "route_selected" || entry.Detail != "" {
		t.Fatalf("unexpected entry fields: %#v", entry)
	}
	if entry.Metadata["schema_version"] != float64(1) || entry.Metadata["run_id"] == "" || entry.Metadata["event_id"] != entry.ID {
		t.Fatalf("missing or invalid injected metadata: %#v", entry.Metadata)
	}
	if entry.Metadata["safe_field"] != "safe-value" {
		t.Fatalf("caller metadata was not preserved: %#v", entry.Metadata)
	}
	if metadata["schema_version"] != 99 || metadata["run_id"] != "caller-value" || metadata["event_id"] != "caller-event" {
		t.Fatalf("caller metadata was mutated: %#v", metadata)
	}
	mainLog, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mainLog), "sensitive-body-or-header") {
		t.Fatalf("ordinary traffic entry was not written to main log: %q", mainLog)
	}
	if strings.Contains(string(mustReadFile(t, path)), "sensitive-body-or-header") {
		t.Fatal("ordinary body/header was mirrored to the diagnostic log")
	}

	second, err := NewTrafficLogger(logFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.AutoDiagnostic("activity_batch", map[string]interface{}{"safe_field": "second"}); err != nil {
		t.Fatal(err)
	}
	second.Close()
	entries = readAutoDiagnosticEntries(t, path)
	if len(entries) != 2 || entries[1].Message != "activity_batch" {
		t.Fatalf("diagnostic file did not append across logger instances: %#v", entries)
	}
}

func TestTrafficLoggerAutoDiagnosticRotation(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "traffic.log")
	logger, err := NewTrafficLogger(logFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := logFile + ".auto-diagnostic.log"
	if err := logger.AutoDiagnostic("before-rotation", nil); err != nil {
		t.Fatal(err)
	}
	logger.autoDiagnosticSize = maxTrafficLogBytes
	if err := logger.AutoDiagnostic("after-rotation", nil); err != nil {
		t.Fatal(err)
	}
	logger.Close()
	current := readAutoDiagnosticEntries(t, path)
	backup := readAutoDiagnosticEntries(t, path+".1")
	if len(current) != 1 || current[0].Message != "after-rotation" {
		t.Fatalf("unexpected current file after rotation: %#v", current)
	}
	if len(backup) != 1 || backup[0].Message != "before-rotation" {
		t.Fatalf("unexpected backup file after rotation: %#v", backup)
	}
}

func TestTrafficLoggerAutoDiagnosticRotationFailurePreservesFiles(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "traffic.log")
	logger, err := NewTrafficLogger(logFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	diagnosticPath := logFile + ".auto-diagnostic.log"
	if err := logger.AutoDiagnostic("before-rotation-error", nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(diagnosticPath)
	if err != nil {
		t.Fatal(err)
	}

	backupPath := diagnosticPath + ".1"
	if err := os.Mkdir(backupPath, 0755); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(backupPath, "sentinel")
	const sentinelContent = "keep backup directory non-empty"
	if err := os.WriteFile(sentinelPath, []byte(sentinelContent), 0644); err != nil {
		t.Fatal(err)
	}
	logger.autoDiagnosticSize = maxTrafficLogBytes
	err = logger.AutoDiagnostic("must-report-rotation-error", nil)
	if err == nil || !strings.Contains(err.Error(), "轮转 Auto 诊断日志失败") {
		t.Fatalf("expected explicit rotation failure, got %v", err)
	}
	afterFailure, err := os.ReadFile(diagnosticPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFailure) != string(before) {
		t.Fatalf("failed rotation changed current diagnostic contents: before=%q after=%q", before, afterFailure)
	}
	sentinel, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(sentinel) != sentinelContent {
		t.Fatalf("rotation failure changed backup sentinel: %q", sentinel)
	}

	// Restore the tracked size after the forced threshold so we can verify the
	// rotation helper reopened the current diagnostic file for later writes.
	logger.autoDiagnosticSize = int64(len(afterFailure))
	if err := logger.AutoDiagnostic("after-rotation-error", nil); err != nil {
		t.Fatalf("diagnostic sink was not usable after failed rotation: %v", err)
	}
	logger.Info("request", "main-after-rotation-error")
	logger.Close()

	entries := readAutoDiagnosticEntries(t, diagnosticPath)
	if len(entries) != 2 || entries[0].Message != "before-rotation-error" || entries[1].Message != "after-rotation-error" {
		t.Fatalf("diagnostic sink did not preserve and append entries: %#v", entries)
	}
	mainLog, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mainLog), "main-after-rotation-error") {
		t.Fatalf("main logger was not usable after diagnostic rotation failure: %q", mainLog)
	}
}

func TestTrafficLoggerAutoDiagnosticDoesNotWaitForMainQueue(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "traffic.log")
	logger, err := NewTrafficLogger(logFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger.fileMu.Lock()
	logger.queueMu.Lock()
	for i := 0; i < cap(logger.queue); i++ {
		logger.queue <- []byte(`{"queued":true}`)
	}
	for len(logger.queue) < cap(logger.queue) {
		logger.queue <- []byte(`{"queued":true}`)
	}
	queued := len(logger.queue)
	if queued != cap(logger.queue) {
		logger.queueMu.Unlock()
		logger.fileMu.Unlock()
		logger.Close()
		t.Fatalf("main queue was not full: len=%d cap=%d", queued, cap(logger.queue))
	}
	if err := logger.AutoDiagnostic("queue-independent", nil); err != nil {
		logger.queueMu.Unlock()
		logger.fileMu.Unlock()
		logger.Close()
		t.Fatalf("diagnostic write waited on the main queue: %v", err)
	}
	logger.queueMu.Unlock()
	logger.fileMu.Unlock()
	logger.Close()
	entries := readAutoDiagnosticEntries(t, logFile+".auto-diagnostic.log")
	if len(entries) != 1 || entries[0].Message != "queue-independent" {
		t.Fatalf("unexpected diagnostic entries: %#v", entries)
	}
}

func TestTrafficLoggerAutoDiagnosticConcurrentClose(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "traffic.log")
	logger, err := NewTrafficLogger(logFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	const writers = 12
	const perWriter = 20
	var successes int
	var resultMu sync.Mutex
	var wg sync.WaitGroup
	for worker := 0; worker < writers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for item := 0; item < perWriter; item++ {
				err := logger.AutoDiagnostic("concurrent", map[string]interface{}{"worker": worker, "item": item})
				if err == nil {
					resultMu.Lock()
					successes++
					resultMu.Unlock()
				}
			}
		}(worker)
	}
	logger.Close()
	wg.Wait()
	logger.Close()
	entries := readAutoDiagnosticEntries(t, logFile+".auto-diagnostic.log")
	if len(entries) != successes {
		t.Fatalf("file has %d entries but %d writes succeeded", len(entries), successes)
	}
	if err := logger.AutoDiagnostic("too-late", nil); err == nil {
		t.Fatal("expected write after Close to fail")
	}
}

func TestTrafficLoggerAutoDiagnosticEmptyPathAndConstructionFailure(t *testing.T) {
	emptyLogger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := emptyLogger.AutoDiagnostic("no-file", nil); err != nil {
		t.Fatal(err)
	}
	emptyLogger.Close()

	logFile := filepath.Join(t.TempDir(), "traffic.log")
	if err := os.Mkdir(logFile+".auto-diagnostic.log", 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTrafficLogger(logFile, nil); err == nil || !strings.Contains(err.Error(), "Auto 诊断日志文件") {
		t.Fatalf("expected clear diagnostic construction error, got %v", err)
	}
	if err := os.Remove(logFile); err != nil && !os.IsNotExist(err) {
		t.Fatalf("main log file remained open after constructor failure: %v", err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
