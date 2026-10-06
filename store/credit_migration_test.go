package store

import (
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// 用旧 schema 创建真实 SQLite 固定样本，避免测试依赖运行中的用户库。
func oldCreditDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"DELETE FROM settings WHERE key = 'credit_unit_version'",
		"DROP TABLE credit_unit_backup_daily",
		"DROP TABLE credit_unit_backup_requests",
		"DROP TABLE credit_unit_backup_settings",
		"INSERT INTO daily_stats (date, model_id, requests, credits) VALUES ('2026-10-01', 'm', 2, 0.08)",
		"INSERT INTO settings (key, value) VALUES ('stat_credits', '0.123456789012345')",
	} {
		if _, err := db.conn.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InsertRequestUsage(&RequestUsage{Credits: .04, InputCost: .04}); err != nil {
		t.Fatal(err)
	}
	return db, path
}

func TestCreditUnitMigrationPreservesBackupAndRunsOnce(t *testing.T) {
	db, path := oldCreditDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var daily, request, cost, backupDaily, backupRequest float64
		for _, query := range []struct {
			sql string
			out *float64
		}{
			{"SELECT credits FROM daily_stats", &daily},
			{"SELECT credits FROM request_usage", &request},
			{"SELECT input_cost FROM request_usage", &cost},
			{"SELECT credits FROM credit_unit_backup_daily", &backupDaily},
			{"SELECT credits FROM credit_unit_backup_requests", &backupRequest},
		} {
			if err := db.conn.QueryRow(query.sql).Scan(query.out); err != nil {
				t.Fatal(err)
			}
		}
		if daily != 2 || request != 1 || cost != .04 || backupDaily != .08 || backupRequest != .04 {
			t.Fatalf("attempt %d: values=%v", attempt, []float64{daily, request, cost, backupDaily, backupRequest})
		}
		value, err := db.GetSetting("stat_credits")
		if err != nil {
			t.Fatal(err)
		}
		total, err := strconv.ParseFloat(value, 64)
		if err != nil || math.Abs(total-3.086419725308625) > 1e-14 {
			t.Fatalf("total=%q error=%v", value, err)
		}
		var oldTotal string
		if err := db.conn.QueryRow("SELECT value FROM credit_unit_backup_settings").Scan(&oldTotal); err != nil || oldTotal != "0.123456789012345" {
			t.Fatalf("backup total=%q error=%v", oldTotal, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCreditUnitMigrationRollsBackOnWriteFailure(t *testing.T) {
	db, _ := oldCreditDB(t)
	defer db.Close()
	if _, err := db.conn.Exec(`CREATE TRIGGER reject_credit_update BEFORE UPDATE OF credits ON request_usage BEGIN SELECT RAISE(ABORT, 'test migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateCreditUnits(); err == nil {
		t.Fatal("expected migration failure")
	}
	var credits float64
	if err := db.conn.QueryRow("SELECT credits FROM daily_stats").Scan(&credits); err != nil || credits != .08 {
		t.Fatalf("partial migration: credits=%v error=%v", credits, err)
	}
	var tables int
	if err := db.conn.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'credit_unit_backup_%'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("backup DDL not rolled back: tables=%d error=%v", tables, err)
	}
	version, err := db.GetSetting(creditUnitVersionKey)
	if err != nil || version != "" {
		t.Fatalf("failed migration recorded version=%q error=%v", version, err)
	}
}

func TestCreditUnitMigrationRejectsInvalidOldTotal(t *testing.T) {
	db, _ := oldCreditDB(t)
	defer db.Close()
	if err := db.SetSetting("stat_credits", "invalid"); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateCreditUnits(); err == nil {
		t.Fatal("invalid old total must not be silently reset")
	}
}

func TestMonthlyCreditsIncludesPersistedAndPendingOnlyThisMonth(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "monthly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stats := NewStats(db)
	defer stats.Close()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))
	for _, row := range []DailyModelStats{
		{Date: "2026-09-30", ModelID: "m", Credits: 100},
		{Date: "2026-10-01", ModelID: "m", Credits: 3.52},
		{Date: "2026-10-07", ModelID: "m", Credits: 100},
	} {
		if err := db.UpsertDailyStats(&row); err != nil {
			t.Fatal(err)
		}
	}
	stats.dailyStats["2026-10-06|m"] = &DailyModelStats{Date: "2026-10-06", ModelID: "m", Credits: .0004}
	for attempt := 0; attempt < 2; attempt++ {
		credits, err := stats.MonthlyCredits(now)
		if err != nil || math.Abs(credits-3.5204) > 1e-12 {
			t.Fatalf("attempt %d: monthly=%v error=%v", attempt, credits, err)
		}
		stats.Flush()
	}
	if err := stats.Clear(); err != nil {
		t.Fatal(err)
	}
	if credits, err := stats.MonthlyCredits(now); err != nil || credits != 0 {
		t.Fatalf("after clear: monthly=%v error=%v", credits, err)
	}
}

func TestStatsPersistsCreditPrecision(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "precision.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stats := NewStats(db)
	const credits = .1711507941956882
	stats.RecordRequestFullWithCredits(0, 0, 0, 0, credits, "m", false)
	stats.Close()
	reloaded := NewStats(db)
	defer reloaded.Close()
	if got := reloaded.Snapshot().Credits; got != credits {
		t.Fatalf("precision lost after restart: got=%v want=%v", got, credits)
	}
}
