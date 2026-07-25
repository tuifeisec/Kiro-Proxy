// db.go
// SQLite 初始化与迁移

package store

import (
	"database/sql"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn                   *sql.DB
	requestUsageCleanupDay atomic.Int64
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
	columns := []struct {
		table string
		name  string
		def   string
	}{
		{"models", "rate_multiplier", "REAL NOT NULL DEFAULT 1.0"},
		{"models", "input_price", "REAL NOT NULL DEFAULT 5.0"},
		{"models", "output_price", "REAL NOT NULL DEFAULT 30.0"},
		{"models", "cache_input_price", "REAL NOT NULL DEFAULT 0.5"},
		{"models", "cache_output_price", "REAL NOT NULL DEFAULT 30.0"},
		{"models", "proxy_url", "TEXT NOT NULL DEFAULT ''"},
		{"models", "enable_continuation", "INTEGER NOT NULL DEFAULT 0"},
		{"models", "disable_auto_suffix", "INTEGER NOT NULL DEFAULT 0"},
		{"models", "refresh_token", "TEXT NOT NULL DEFAULT ''"},
		{"models", "codex_email", "TEXT NOT NULL DEFAULT ''"},
		{"models", "codex_plan_type", "TEXT NOT NULL DEFAULT ''"},
		{"models", "codex_account_id", "TEXT NOT NULL DEFAULT ''"},
		{"models", "access_token_expires_at", "INTEGER NOT NULL DEFAULT 0"},
		{"models", "codex_usage_json", "TEXT NOT NULL DEFAULT ''"},
		{"models", "service_tier", "TEXT NOT NULL DEFAULT ''"},
		{"daily_stats", "credits", "REAL NOT NULL DEFAULT 0"},
		{"request_usage", "credits", "REAL NOT NULL DEFAULT 0"},
		{"request_usage", "input_cost", "REAL NOT NULL DEFAULT 0"},
		{"request_usage", "output_cost", "REAL NOT NULL DEFAULT 0"},
		{"request_usage", "cache_read_cost", "REAL NOT NULL DEFAULT 0"},
		{"request_usage", "cache_write_cost", "REAL NOT NULL DEFAULT 0"},
		{"request_usage", "conversion_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"request_usage", "auth_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"request_usage", "dns_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"request_usage", "connect_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"request_usage", "tls_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"request_usage", "request_send_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"request_usage", "upstream_wait_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"request_usage", "response_header_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"request_usage", "stream_wait_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"request_usage", "retry_count", "INTEGER NOT NULL DEFAULT 0"},
		{"request_usage", "reused_connection", "INTEGER NOT NULL DEFAULT 0"},
		{"request_usage", "reasoning_effort", "TEXT NOT NULL DEFAULT ''"},
		{"request_usage", "service_tier", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, col := range columns {
		if err := db.addColumnIfNotExists(col.table, col.name, col.def); err != nil {
			return err
		}
	}

	// 启动时清理过期数据。
	db.CleanExpiredResponsesSessions()
	if err := db.cleanupExpiredRequestUsage(time.Now()); err != nil {
		log.Printf("[store] 清理过期请求统计失败: %v", err)
	}
	return nil
}

func (db *DB) addColumnIfNotExists(table, column, colDef string) error {
	rows, err := db.conn.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("读取表结构失败 %s.%s: %w", table, column, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("解析表结构失败 %s.%s: %w", table, column, err)
		}
		if name == column {
			return nil // 列已存在
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历表结构失败 %s.%s: %w", table, column, err)
	}
	if _, err := db.conn.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + colDef); err != nil {
		return fmt.Errorf("添加列失败 %s.%s: %w", table, column, err)
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS models (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL DEFAULT '',
    model_id        TEXT NOT NULL,
    provider        TEXT NOT NULL DEFAULT 'openai',
    base_url        TEXT NOT NULL,
    api_key         TEXT NOT NULL,
    refresh_token   TEXT NOT NULL DEFAULT '',
    proxy_url       TEXT NOT NULL DEFAULT '',
    endpoint        TEXT NOT NULL DEFAULT '',
    disable_auto_suffix INTEGER NOT NULL DEFAULT 0,
    context_window  INTEGER NOT NULL DEFAULT 0,
    max_tokens      INTEGER NOT NULL DEFAULT 0,
    reasoning_effort TEXT NOT NULL DEFAULT '',
    rate_multiplier REAL NOT NULL DEFAULT 1.0,
    input_price     REAL NOT NULL DEFAULT 5.0,
    output_price    REAL NOT NULL DEFAULT 30.0,
    cache_input_price REAL NOT NULL DEFAULT 0.5,
    cache_output_price REAL NOT NULL DEFAULT 30.0,
    enable_continuation INTEGER NOT NULL DEFAULT 0,
    codex_email     TEXT NOT NULL DEFAULT '',
    codex_plan_type TEXT NOT NULL DEFAULT '',
    codex_account_id TEXT NOT NULL DEFAULT '',
    access_token_expires_at INTEGER NOT NULL DEFAULT 0,
    codex_usage_json TEXT NOT NULL DEFAULT '',
    service_tier   TEXT NOT NULL DEFAULT '',
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

CREATE TABLE IF NOT EXISTS request_usage (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    request_date       TEXT NOT NULL,
    created_at         TEXT NOT NULL,
    model_id           TEXT NOT NULL DEFAULT '',
    provider           TEXT NOT NULL DEFAULT '',
    endpoint           TEXT NOT NULL DEFAULT '',
    request_path       TEXT NOT NULL DEFAULT '',
    protocol           TEXT NOT NULL DEFAULT '',
    status_code        INTEGER NOT NULL DEFAULT 0,
    success            INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    first_byte_ms      INTEGER NOT NULL DEFAULT 0,
    duration_ms        INTEGER NOT NULL DEFAULT 0,
    credits            REAL NOT NULL DEFAULT 0,
    input_cost         REAL NOT NULL DEFAULT 0,
    output_cost        REAL NOT NULL DEFAULT 0,
    cache_read_cost    REAL NOT NULL DEFAULT 0,
    cache_write_cost   REAL NOT NULL DEFAULT 0,
    conversion_ms      INTEGER NOT NULL DEFAULT 0,
    auth_ms            INTEGER NOT NULL DEFAULT 0,
    dns_ms             INTEGER NOT NULL DEFAULT 0,
    connect_ms         INTEGER NOT NULL DEFAULT 0,
    tls_ms             INTEGER NOT NULL DEFAULT 0,
    request_send_ms    INTEGER NOT NULL DEFAULT 0,
    upstream_wait_ms   INTEGER NOT NULL DEFAULT 0,
    response_header_ms INTEGER NOT NULL DEFAULT 0,
    stream_wait_ms     INTEGER NOT NULL DEFAULT 0,
		retry_count        INTEGER NOT NULL DEFAULT 0,
		reused_connection  INTEGER NOT NULL DEFAULT 0,
	reasoning_effort   TEXT NOT NULL DEFAULT '',
	service_tier       TEXT NOT NULL DEFAULT '',
    error_message      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_request_usage_date_id ON request_usage(request_date, id DESC);
CREATE INDEX IF NOT EXISTS idx_request_usage_created_at ON request_usage(created_at DESC, id DESC);

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
