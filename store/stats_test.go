package store

import "testing"

func TestStatsClearRemovesRequestUsage(t *testing.T) {
	db, err := Open(t.TempDir() + "/stats-clear.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stats := NewStats(db)
	defer stats.Close()
	stats.RecordRequestFull(10, 20, 1, 2, "model", false)
	if err := db.InsertRequestUsage(&RequestUsage{ModelID: "model", Success: true}); err != nil {
		t.Fatal(err)
	}

	if err := stats.Clear(); err != nil {
		t.Fatal(err)
	}
	page, err := db.QueryRequestUsagePage(RequestUsageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || page.Summary.Requests != 0 {
		t.Fatalf("request usage remains after clear: total=%d summary=%+v", page.Total, page.Summary)
	}
	if daily := stats.GetDailyStats("0000-01-01", "9999-12-31"); len(daily) != 0 {
		t.Fatalf("daily stats remain after clear: %+v", daily)
	}
}
