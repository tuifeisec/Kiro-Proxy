package store

import (
	"testing"
	"time"
)

func TestStatsDayUsesSixAMBoundary(t *testing.T) {
	location := time.FixedZone("test", 8*60*60)
	if got := statsDay(time.Date(2026, 9, 13, 5, 59, 59, 0, location)); got != "2026-09-12" {
		t.Fatalf("statsDay before boundary = %q", got)
	}
	if got := statsDay(time.Date(2026, 9, 13, 6, 0, 0, 0, location)); got != "2026-09-13" {
		t.Fatalf("statsDay at boundary = %q", got)
	}
}

func TestRequestUsageDefaultsToStatsDay(t *testing.T) {
	db, err := Open(t.TempDir() + "/request-date.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.InsertRequestUsage(&RequestUsage{}); err != nil {
		t.Fatal(err)
	}
	page, err := db.QueryRequestUsagePage(RequestUsageQuery{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items=%d", len(page.Items))
	}
	if got, want := page.Items[0].RequestDate, statsDay(time.Now()); got != want {
		t.Fatalf("request date = %q, want current stats day %q", got, want)
	}
}

func TestRequestUsageAverageIncludesMeasuredZero(t *testing.T) {
	db, err := Open(t.TempDir() + "/zero.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ms := range []int64{0, 10} {
		if err := db.InsertRequestUsage(&RequestUsage{FirstContentMS: ms, ContentMeasured: true, ContentMeasurementComplete: true}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := db.QueryRequestUsagePage(RequestUsageQuery{})
	if err != nil || page.Summary.AverageFirstContent != 5 || page.Summary.ContentSamples != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestRequestUsagePersistsContentAndAttemptMetrics(t *testing.T) {
	db, err := Open(t.TempDir() + "/usage.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	items := []*RequestUsage{
		{
			ModelID: "model-a", Provider: "openai", StatusCode: 200, Success: true,
			FirstByteMS: 12, FirstEventMS: 15, FirstContentMS: 220, FirstContentType: "text",
			MaxContentGapMS: 80, ContentMeasured: true, ContentMeasurementComplete: true,
			DurationMS: 500, ConversionMS: 3, AuditMS: 4, SessionReadMS: 5,
			WireAttemptCount: 2, TransportFallbackCount: 1,
		},
		{
			ModelID: "model-b", Provider: "openai", StatusCode: 502, Success: false,
			FirstByteMS: 9, DurationMS: 40,
		},
		{
			ModelID: "model-c", Provider: "openai", StatusCode: 200, Success: true,
			FirstContentMS: 40, ContentMeasured: true, ContentMeasurementComplete: false,
			DurationMS: 70,
		},
	}
	for _, item := range items {
		if err := db.InsertRequestUsage(item); err != nil {
			t.Fatal(err)
		}
	}

	page, err := db.QueryRequestUsagePage(RequestUsageQuery{PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("items=%d", len(page.Items))
	}
	var measured, unknown, partial RequestUsage
	for _, item := range page.Items {
		switch item.ModelID {
		case "model-a":
			measured = item
		case "model-b":
			unknown = item
		case "model-c":
			partial = item
		}
	}
	if measured.FirstEventMS != 15 || measured.FirstContentMS != 220 || measured.FirstContentType != "text" || measured.MaxContentGapMS != 80 ||
		!measured.ContentMeasured || !measured.ContentMeasurementComplete || measured.AuditMS != 4 || measured.SessionReadMS != 5 ||
		measured.WireAttemptCount != 2 || measured.TransportFallbackCount != 1 {
		t.Fatalf("measured item lost metrics: %+v", measured)
	}
	if unknown.ContentMeasured || unknown.ContentMeasurementComplete || unknown.FirstContentMS != 0 {
		t.Fatalf("unknown item became measured: %+v", unknown)
	}
	if !partial.ContentMeasured || partial.ContentMeasurementComplete || partial.FirstContentMS != 40 {
		t.Fatalf("partial item lost status: %+v", partial)
	}
	if page.Summary.AverageFirstContent != 220 || page.Summary.ContentSamples != 1 {
		t.Fatalf("summary=%+v", page.Summary)
	}
}
