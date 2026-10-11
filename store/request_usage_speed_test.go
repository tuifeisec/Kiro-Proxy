package store

import (
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenSpeedFormulaAndStatuses(t *testing.T) {
	valid := RequestUsage{
		Success: true, ContentMeasured: true, ContentMeasurementComplete: true,
		SpeedOutputTokens: 162, SpeedDurationNS: 1_000_000_000, SpeedBasis: "visible_output",
		DurationMS: 1000,
	}
	if got := valid.calculateTokensPerSecond(); got == nil || *got != 161 {
		t.Fatalf("GPT speed = %v, want 161 tokens/s", got)
	}
	valid.SpeedOutputTokens = 4
	valid.SpeedDurationNS = 250_000
	if got := valid.calculateTokensPerSecond(); got == nil || *got != 12_000 {
		t.Fatalf("sub-ms speed = %v, want 12000 tokens/s", got)
	}

	tests := []struct {
		name string
		item RequestUsage
		want string
	}{
		{"legacy", RequestUsage{Success: true}, "legacy"},
		{"failed takes priority", RequestUsage{SpeedBasis: "unmeasured"}, "failed"},
		{"unmeasured", RequestUsage{Success: true, SpeedBasis: "unmeasured"}, "unmeasured"},
		{"no content", RequestUsage{Success: true, SpeedBasis: "visible_output"}, "no_content"},
		{"partial", RequestUsage{Success: true, ContentMeasured: true, SpeedBasis: "visible_output"}, "incomplete"},
		{"missing usage", RequestUsage{Success: true, ContentMeasured: true, ContentMeasurementComplete: true, SpeedOutputTokens: -1, SpeedBasis: "visible_output"}, "usage_missing"},
		{"unsupported usage", RequestUsage{Success: true, ContentMeasured: true, ContentMeasurementComplete: true, SpeedOutputTokens: 10, SpeedBasis: "unknown"}, "usage_missing"},
		{"upstream reported output without reasoning details", RequestUsage{Success: true, ContentMeasured: true, ContentMeasurementComplete: true, SpeedOutputTokens: 10, SpeedDurationNS: 1_000_000, DurationMS: 1, SpeedBasis: "reported_output"}, "ok"},
		{"one token", RequestUsage{Success: true, ContentMeasured: true, ContentMeasurementComplete: true, SpeedOutputTokens: 1, SpeedDurationNS: 1, SpeedBasis: "visible_output"}, "insufficient_tokens"},
		{"zero interval", RequestUsage{Success: true, ContentMeasured: true, ContentMeasurementComplete: true, SpeedOutputTokens: 2, SpeedBasis: "visible_output"}, "insufficient_interval"},
		{"interval exceeds request", RequestUsage{Success: true, ContentMeasured: true, ContentMeasurementComplete: true, DurationMS: 1, SpeedOutputTokens: 2, SpeedDurationNS: 2_000_001, SpeedBasis: "visible_output"}, "insufficient_interval"},
		{"millisecond truncation allowance", RequestUsage{Success: true, ContentMeasured: true, ContentMeasurementComplete: true, DurationMS: 1, SpeedOutputTokens: 2, SpeedDurationNS: 2_000_000, SpeedBasis: "visible_output"}, "ok"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.item.tokenRateStatus(); got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
			if test.want != "ok" && test.item.calculateTokensPerSecond() != nil {
				t.Fatal("invalid sample produced a token rate")
			}
		})
	}
}

func TestTokenSpeedPersistenceAndDailyIsolation(t *testing.T) {
	db, err := Open(t.TempDir() + "/token-speed.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.conn.Exec(`
		INSERT INTO daily_stats (date, model_id, generation_speed_sum, generation_speed_samples)
		VALUES ('2026-10-10', 'model', 7.5, 2)
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertDailyStats(&DailyModelStats{Date: "2026-10-10", ModelID: "model", Requests: 1}); err != nil {
		t.Fatal(err)
	}

	item := &RequestUsage{
		RequestDate: "2026-10-10", ModelID: "model", Provider: "openai", Success: true,
		OutputTokens: 1186, ContentMeasured: true, ContentMeasurementComplete: true,
		SpeedOutputTokens: 162, SpeedDurationNS: 1_000_000_000, SpeedBasis: "visible_output",
		DurationMS: 2000,
	}
	if err := db.InsertRequestUsage(item); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertDailyStats(&DailyModelStats{Date: "2026-10-10", ModelID: "model", Requests: 2}); err != nil {
		t.Fatal(err)
	}
	page, err := db.QueryRequestUsagePage(RequestUsageQuery{StartDate: "2026-10-10", EndDate: "2026-10-10"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(page.Items))
	}
	got := page.Items[0]
	if got.OutputTokens != 1186 || got.SpeedOutputTokens != 162 || got.SpeedDurationNS != 1_000_000_000 || got.SpeedBasis != "visible_output" ||
		got.TokenRateStatus != "ok" || got.TokensPerSecond == nil || *got.TokensPerSecond != 161 {
		t.Fatalf("persisted speed metrics = %+v", got)
	}

	if _, err := db.conn.Exec(`
		INSERT INTO request_usage (request_date, created_at, success, output_tokens, content_measured, content_measurement_complete)
		VALUES ('2026-10-10', '2026-10-10T12:00:00Z', 1, 100, 1, 1)
	`); err != nil {
		t.Fatal(err)
	}
	page, err = db.QueryRequestUsagePage(RequestUsageQuery{StartDate: "2026-10-10", EndDate: "2026-10-10"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items after adding legacy row = %+v", page.Items)
	}
	var legacyFound bool
	for _, row := range page.Items {
		if row.SpeedBasis == "" {
			legacyFound = row.TokenRateStatus == "legacy" && row.TokensPerSecond == nil && row.SpeedOutputTokens == -1
		}
	}
	if !legacyFound {
		t.Fatalf("legacy request was reinterpreted: %+v", page.Items)
	}

	secondDay := &RequestUsage{
		RequestDate: "2026-10-11", ModelID: "model", Success: true,
		ContentMeasured: true, ContentMeasurementComplete: true,
		SpeedOutputTokens: 3, SpeedDurationNS: 1_000_000_000, SpeedBasis: "visible_output",
		DurationMS: 1000,
	}
	if err := db.InsertRequestUsage(secondDay); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryDailyStats("2026-10-10", "2026-10-11")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("daily rows = %+v", rows)
	}
	var combinedSpeedSum float64
	var combinedSpeedSamples int64
	for _, row := range rows {
		combinedSpeedSum += row.GenerationSpeedSum
		combinedSpeedSamples += row.GenerationSpeedSamples
		if row.Date == "2026-10-10" {
			if math.Abs(row.GenerationSpeedSum-161) > 1e-9 || row.GenerationSpeedSamples != 1 {
				t.Fatalf("v2 daily values on first day = %+v", row)
			}
		} else if row.Date == "2026-10-11" {
			if row.GenerationSpeedSum != 2 || row.GenerationSpeedSamples != 1 {
				t.Fatalf("v2 daily values on second day = %+v", row)
			}
		}
	}
	if combinedSpeedSum != 163 || combinedSpeedSamples != 2 {
		t.Fatalf("cross-day speed aggregate = sum %v, samples %d", combinedSpeedSum, combinedSpeedSamples)
	}

	var legacySum float64
	var legacySamples int64
	if err := db.conn.QueryRow(`SELECT generation_speed_sum, generation_speed_samples FROM daily_stats WHERE date = '2026-10-10' AND model_id = 'model'`).Scan(&legacySum, &legacySamples); err != nil {
		t.Fatal(err)
	}
	if legacySum != 7.5 || legacySamples != 2 {
		t.Fatalf("legacy daily values changed: sum=%v samples=%d", legacySum, legacySamples)
	}

	if err := db.ClearUsageStats(); err != nil {
		t.Fatal(err)
	}
	page, err = db.QueryRequestUsagePage(RequestUsageQuery{})
	if err != nil || page.Total != 0 {
		t.Fatalf("requests after ClearUsageStats = %d, err=%v", page.Total, err)
	}
	rows, err = db.QueryDailyStats("0000-01-01", "9999-12-31")
	if err != nil || len(rows) != 0 {
		t.Fatalf("daily stats after ClearUsageStats = %+v, err=%v", rows, err)
	}
}

func TestTokenSpeedMigratesLegacySchema(t *testing.T) {
	oldSchema := strings.NewReplacer(
		"    generation_speed_v2_sum REAL NOT NULL DEFAULT 0,\n", "",
		"    generation_speed_v2_samples INTEGER NOT NULL DEFAULT 0,\n", "",
		"    speed_output_tokens INTEGER NOT NULL DEFAULT -1,\n", "",
		"    speed_duration_ns INTEGER NOT NULL DEFAULT 0,\n", "",
		"    speed_basis TEXT NOT NULL DEFAULT '',\n", "",
	).Replace(schema)
	for _, newColumn := range []string{
		"generation_speed_v2_sum", "generation_speed_v2_samples",
		"speed_output_tokens", "speed_duration_ns", "speed_basis",
	} {
		if strings.Contains(oldSchema, newColumn) {
			t.Fatalf("old schema still contains new column %q", newColumn)
		}
	}

	filename := filepath.Join(t.TempDir(), "legacy.db")
	legacyDB, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.Exec(oldSchema); err != nil {
		legacyDB.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	day := statsDay(time.Now())
	if _, err := legacyDB.Exec(`
		INSERT INTO request_usage (
			request_date, created_at, model_id, provider, status_code, success,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			credits, input_cost, output_cost
		) VALUES (?, ?, 'legacy-model', 'openai', 200, 1, 23, 42, 5, 2, 0.75, 1.25, 2.5)
	`, day, time.Now().Format(time.RFC3339Nano)); err != nil {
		legacyDB.Close()
		t.Fatalf("insert legacy request: %v", err)
	}
	if _, err := legacyDB.Exec(`
		INSERT INTO daily_stats (
			date, model_id, requests, input_tokens, output_tokens, cache_read, cache_write,
			credits, generation_speed_sum, generation_speed_samples
		) VALUES (?, 'legacy-model', 3, 23, 42, 5, 2, 0.75, 12.5, 4)
	`, day); err != nil {
		legacyDB.Close()
		t.Fatalf("insert legacy daily stats: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	db, err := Open(filename)
	if err != nil {
		t.Fatalf("open and migrate legacy database: %v", err)
	}
	defer db.Close()

	page, err := db.QueryRequestUsagePage(RequestUsageQuery{StartDate: day, EndDate: day})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("migrated request count = %d, want 1", len(page.Items))
	}
	request := page.Items[0]
	if request.TokenRateStatus != "legacy" || request.TokensPerSecond != nil || request.SpeedOutputTokens != -1 ||
		request.SpeedDurationNS != 0 || request.SpeedBasis != "" {
		t.Fatalf("migrated speed fields = %+v", request)
	}
	if request.ModelID != "legacy-model" || request.InputTokens != 23 || request.OutputTokens != 42 ||
		request.CacheReadTokens != 5 || request.CacheWriteTokens != 2 || request.Credits != 18.75 ||
		request.InputCost != 1.25 || request.OutputCost != 2.5 {
		t.Fatalf("legacy request usage or costs changed: %+v", request)
	}
	var backedUpRequestCredits float64
	if err := db.conn.QueryRow(`SELECT credits FROM credit_unit_backup_requests WHERE id = ?`, request.ID).Scan(&backedUpRequestCredits); err != nil {
		t.Fatal(err)
	}
	if backedUpRequestCredits != 0.75 {
		t.Fatalf("legacy request credits backup = %v, want 0.75", backedUpRequestCredits)
	}

	rows, err := db.QueryDailyStats(day, day)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("migrated daily rows = %+v", rows)
	}
	daily := rows[0]
	if daily.GenerationSpeedSum != 0 || daily.GenerationSpeedSamples != 0 || daily.Requests != 3 ||
		daily.InputTokens != 23 || daily.OutputTokens != 42 || daily.CacheRead != 5 || daily.CacheWrite != 2 || daily.Credits != 18.75 {
		t.Fatalf("migrated daily stats = %+v", daily)
	}
	var backedUpDailyCredits float64
	if err := db.conn.QueryRow(`
		SELECT credits FROM credit_unit_backup_daily WHERE date = ? AND model_id = 'legacy-model'
	`, day).Scan(&backedUpDailyCredits); err != nil {
		t.Fatal(err)
	}
	if backedUpDailyCredits != 0.75 {
		t.Fatalf("legacy daily credits backup = %v, want 0.75", backedUpDailyCredits)
	}
	var legacySpeedSum float64
	var legacySpeedSamples int64
	if err := db.conn.QueryRow(`
		SELECT generation_speed_sum, generation_speed_samples FROM daily_stats WHERE date = ? AND model_id = 'legacy-model'
	`, day).Scan(&legacySpeedSum, &legacySpeedSamples); err != nil {
		t.Fatal(err)
	}
	if legacySpeedSum != 12.5 || legacySpeedSamples != 4 {
		t.Fatalf("legacy daily speed changed: sum=%v samples=%d", legacySpeedSum, legacySpeedSamples)
	}
}
