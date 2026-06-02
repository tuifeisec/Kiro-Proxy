// db.go
// SQLite 初始化与迁移

package store

import (
	"database/sql"
	"fmt"
	"log"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
}

func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on", path)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}

	log.Printf("[store] 数据库已打开: %s", path)
	return db, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) migrate() error {
	_, err := db.conn.Exec(schema)
	if err != nil {
		return err
	}
	// 增量迁移：为已有表添加新列（IF NOT EXISTS 不支持 ALTER，用 pragma 检测）
	db.addColumnIfNotExists("models", "rate_multiplier", "REAL NOT NULL DEFAULT 1.0")
	db.addColumnIfNotExists("models", "input_price", "REAL NOT NULL DEFAULT 5.0")
	db.addColumnIfNotExists("models", "output_price", "REAL NOT NULL DEFAULT 30.0")
	db.addColumnIfNotExists("models", "cache_input_price", "REAL NOT NULL DEFAULT 0.5")
	db.addColumnIfNotExists("models", "cache_output_price", "REAL NOT NULL DEFAULT 30.0")
	db.addColumnIfNotExists("models", "proxy_url", "TEXT NOT NULL DEFAULT ''")
	db.addColumnIfNotExists("models", "enable_continuation", "INTEGER NOT NULL DEFAULT 0")
	db.addColumnIfNotExists("daily_stats", "credits", "REAL NOT NULL DEFAULT 0")

	// 启动时清理过期 session
	db.CleanExpiredResponsesSessions()
	return nil
}

func (db *DB) addColumnIfNotExists(table, column, colDef string) {
	rows, err := db.conn.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return
		}
		if name == column {
			return // 列已存在
		}
	}
	db.conn.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + colDef)
}

const schema = `
CREATE TABLE IF NOT EXISTS models (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL DEFAULT '',
    model_id        TEXT NOT NULL,
    provider        TEXT NOT NULL DEFAULT 'openai',
    base_url        TEXT NOT NULL,
    api_key         TEXT NOT NULL,
    proxy_url       TEXT NOT NULL DEFAULT '',
    endpoint        TEXT NOT NULL DEFAULT '',
    context_window  INTEGER NOT NULL DEFAULT 0,
    max_tokens      INTEGER NOT NULL DEFAULT 0,
    reasoning_effort TEXT NOT NULL DEFAULT '',
    rate_multiplier REAL NOT NULL DEFAULT 1.0,
    input_price     REAL NOT NULL DEFAULT 5.0,
    output_price    REAL NOT NULL DEFAULT 30.0,
    cache_input_price REAL NOT NULL DEFAULT 0.5,
    cache_output_price REAL NOT NULL DEFAULT 30.0,
    enable_continuation INTEGER NOT NULL DEFAULT 0,
    enabled         INTEGER NOT NULL DEFAULT 1,
    sort_order      INTEGER NOT NULL DEFAULT 0,
    note            TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS daily_stats (
    date          TEXT NOT NULL,
    model_id      TEXT NOT NULL,
    requests      INTEGER NOT NULL DEFAULT 0,
    errors        INTEGER NOT NULL DEFAULT 0,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read    INTEGER NOT NULL DEFAULT 0,
    cache_write   INTEGER NOT NULL DEFAULT 0,
    credits       REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (date, model_id)
);

CREATE TABLE IF NOT EXISTS responses_sessions (
    conversation_id TEXT PRIMARY KEY,
    input_hash      TEXT NOT NULL DEFAULT '',
    response_id     TEXT NOT NULL DEFAULT '',
    input_json      TEXT NOT NULL DEFAULT '',
    item_count      INTEGER NOT NULL DEFAULT 0,
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT OR IGNORE INTO settings (key, value) VALUES
    ('proxy_port',          '18081'),
    ('upstream_proxy_url',  ''),
    ('direct_mode',         'false');
`
