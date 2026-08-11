package test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"kiro-proxy/store"
)

func TestStatsConcurrentFlushAndClearPersistence(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "stats-stability.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stats := store.NewStats(db)
	const (
		workers   = 8
		perWorker = 125
	)
	done := make(chan struct{})
	var flushWG sync.WaitGroup
	flushWG.Add(1)
	go func() {
		defer flushWG.Done()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				stats.Flush()
			case <-done:
				return
			}
		}
	}()

	var recordWG sync.WaitGroup
	for i := 0; i < workers; i++ {
		recordWG.Add(1)
		go func() {
			defer recordWG.Done()
			for j := 0; j < perWorker; j++ {
				stats.RecordRequestFull(1, 2, 0, 0, "model", false)
			}
		}()
	}
	recordWG.Wait()
	close(done)
	flushWG.Wait()
	stats.Close()

	reloaded := store.NewStats(db)
	snapshot := reloaded.Snapshot()
	if snapshot.TotalRequests != workers*perWorker || snapshot.InputTokens != workers*perWorker || snapshot.OutputTokens != workers*perWorker*2 {
		t.Fatalf("persisted totals mismatch: %#v", snapshot)
	}
	start := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	end := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	dailyRequests := int64(0)
	for _, row := range reloaded.GetDailyStats(start, end) {
		if row.ModelID == "model" {
			dailyRequests += row.Requests
		}
	}
	if dailyRequests != workers*perWorker {
		t.Fatalf("persisted daily requests mismatch: got %d", dailyRequests)
	}

	if err := reloaded.Clear(); err != nil {
		t.Fatal(err)
	}
	reloaded.RecordRequestFull(3, 4, 0, 0, "model", false)
	reloaded.Close()

	afterClear := store.NewStats(db)
	defer afterClear.Close()
	clearedSnapshot := afterClear.Snapshot()
	if clearedSnapshot.TotalRequests != 1 || clearedSnapshot.InputTokens != 3 || clearedSnapshot.OutputTokens != 4 {
		t.Fatalf("clear persistence mismatch: %#v", clearedSnapshot)
	}
	dailyRequests = 0
	for _, row := range afterClear.GetDailyStats(start, end) {
		if row.ModelID == "model" {
			dailyRequests += row.Requests
		}
	}
	if dailyRequests != 1 {
		t.Fatalf("daily requests after clear mismatch: got %d", dailyRequests)
	}
}

func TestStatsRetainsDailyIncrementWhenDatabaseWriteFails(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "stats-write-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	stats := store.NewStats(db)
	stats.RecordRequestFull(1, 2, 0, 0, "model", false)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	stats.Flush()
	defer stats.Close()

	start := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	end := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	dailyRequests := int64(0)
	for _, row := range stats.GetDailyStats(start, end) {
		if row.ModelID == "model" {
			dailyRequests += row.Requests
		}
	}
	if dailyRequests != 1 {
		t.Fatalf("failed daily increment was lost: got %d", dailyRequests)
	}
}
