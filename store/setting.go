// setting.go
// 键值设置的读写（代理端口、直连模式等）

package store

import (
	"database/sql"
	"errors"
	"sort"
	"time"
)

func (db *DB) GetSetting(key string) (string, error) {
	var value string
	err := db.conn.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (db *DB) SetSetting(key, value string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.conn.Exec(`
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
	`, key, value, now)
	return err
}

func (db *DB) SetSettings(settings map[string]string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	const upsertSQL = `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
	`
	for _, key := range keys {
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := tx.Exec(upsertSQL, key, settings[key], now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) GetAllSettings() (map[string]string, error) {
	rows, err := db.conn.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		result[k] = v
	}
	return result, rows.Err()
}
