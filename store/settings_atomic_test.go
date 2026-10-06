package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func newAtomicSettingsTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func TestSetSettingsUpdatesAndInsertsAtomically(t *testing.T) {
	db := newAtomicSettingsTestDB(t)
	if err := db.SetSetting("existing", "before"); err != nil {
		t.Fatal(err)
	}

	if err := db.SetSettings(map[string]string{
		"existing": "after",
		"inserted": "new value",
	}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}

	for key, want := range map[string]string{
		"existing": "after",
		"inserted": "new value",
	} {
		got, err := db.GetSetting(key)
		if err != nil || got != want {
			t.Errorf("GetSetting(%q) = %q, error=%v; want %q", key, got, err, want)
		}
	}
}

func TestSetSettingsRollsBackWhenLaterWriteFails(t *testing.T) {
	db := newAtomicSettingsTestDB(t)
	for key, value := range map[string]string{
		"a_first": "before first",
		"z_fail":  "before failure",
	} {
		if err := db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}

	_, err := db.conn.Exec(`
		CREATE TRIGGER fail_later_setting
		BEFORE UPDATE ON settings
		WHEN NEW.key = 'z_fail'
			AND (SELECT value FROM settings WHERE key = 'a_first') = 'after first'
		BEGIN
			SELECT RAISE(ABORT, 'mock settings write failure');
		END;
	`)
	if err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	err = db.SetSettings(map[string]string{
		"a_first": "after first",
		"z_fail":  "after failure",
	})
	if err == nil || !strings.Contains(err.Error(), "mock settings write failure") {
		t.Fatalf("SetSettings error = %v, want original trigger error", err)
	}

	for key, want := range map[string]string{
		"a_first": "before first",
		"z_fail":  "before failure",
	} {
		got, readErr := db.GetSetting(key)
		if readErr != nil || got != want {
			t.Errorf("GetSetting(%q) = %q, error=%v; want rolled-back value %q", key, got, readErr, want)
		}
	}
}
