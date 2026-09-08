// Responses 会话状态与并发代次。previous_response_id 不保证缓存命中率。
package store

import (
	"database/sql"
	"log"
	"time"
)

const ResponsesSessionTTL = 7 * 24 * time.Hour
const MaxResponsesSessionBytes = 4 << 20

type ResponsesSession struct {
	StateKey   string
	InputHash  string
	ResponseID string
	InputJSON  string
	ItemCount  int
	UpdatedAt  time.Time
	Generation int64
}

// UpsertResponsesSession 用于直接导入状态，同时使已发出的旧写入代次失效。
func (db *DB) UpsertResponsesSession(s *ResponsesSession) error {
	if len(s.InputJSON) > MaxResponsesSessionBytes {
		return nil
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = time.Now()
	}
	_, err := db.conn.Exec(`
 INSERT INTO responses_sessions (conversation_id,input_hash,response_id,input_json,item_count,updated_at,generation)
 VALUES (?,?,?,?,?,?,1)
 ON CONFLICT(conversation_id) DO UPDATE SET
 input_hash=excluded.input_hash,response_id=excluded.response_id,input_json=excluded.input_json,
 item_count=excluded.item_count,updated_at=excluded.updated_at,generation=responses_sessions.generation+1
 `, s.StateKey, s.InputHash, s.ResponseID, s.InputJSON, s.ItemCount, s.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

type responsesSessionScanner interface{ Scan(...interface{}) error }

func scanResponsesSession(row responsesSessionScanner, now time.Time) (*ResponsesSession, error) {
	var s ResponsesSession
	var updated string
	err := row.Scan(&s.StateKey, &s.InputHash, &s.ResponseID, &s.InputJSON, &s.ItemCount, &updated, &s.Generation)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		s.UpdatedAt, err = time.Parse("2006-01-02 15:04:05", updated)
	}
	if err != nil || now.Sub(s.UpdatedAt) >= ResponsesSessionTTL || len(s.InputJSON) > MaxResponsesSessionBytes || s.ResponseID == "" {
		return nil, nil
	}
	return &s, nil
}

const selectResponsesSession = `SELECT conversation_id,input_hash,response_id,input_json,item_count,updated_at,generation FROM responses_sessions WHERE conversation_id=?`

func (db *DB) GetResponsesSession(key string) (*ResponsesSession, error) {
	return scanResponsesSession(db.conn.QueryRow(selectResponsesSession, key), time.Now())
}

// BeginResponsesSession 为本次发送分配单调代次，不在网络请求期间持有锁或事务。
// 同一会话允许分叉请求；只有最新发起的一次能发布后续可复用状态。
func (db *DB) BeginResponsesSession(key string) (*ResponsesSession, int64, error) {
	now := time.Now()
	if err := db.cleanResponsesSessionsAt(now); err != nil {
		return nil, 0, err
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	previous, err := scanResponsesSession(tx.QueryRow(selectResponsesSession, key), now)
	if err != nil {
		return nil, 0, err
	}
	// 使用 DB 实例级序号，删除/清理后重新插入也不能复用在飞请求的代次。
	generation := db.responsesGeneration.Add(1)
	_, err = tx.Exec(`INSERT INTO responses_sessions(conversation_id,updated_at,generation) VALUES(?,?,?)
 ON CONFLICT(conversation_id) DO UPDATE SET generation=excluded.generation`,
		key, now.Format(time.RFC3339Nano), generation)
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	return previous, generation, nil
}

// CommitResponsesSession 返回 false 表示已被较新请求替代或快照超预算；不影响响应内容。
func (db *DB) CommitResponsesSession(s *ResponsesSession, generation int64) (bool, error) {
	if generation <= 0 || len(s.InputJSON) > MaxResponsesSessionBytes {
		return false, nil
	}
	result, err := db.conn.Exec(`UPDATE responses_sessions SET input_hash=?,response_id=?,input_json=?,item_count=?,updated_at=?
 WHERE conversation_id=? AND generation=?`,
		s.InputHash, s.ResponseID, s.InputJSON, s.ItemCount, time.Now().Format(time.RFC3339Nano), s.StateKey, generation)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// 只清除本次代次看到的失效 response，保留代次让本次全量回退可以提交。
func (db *DB) InvalidateResponsesSession(key string, generation int64) error {
	_, err := db.conn.Exec(`UPDATE responses_sessions SET response_id='',input_json='',input_hash='',item_count=0
 WHERE conversation_id=? AND generation=?`, key, generation)
	return err
}

func (db *DB) DeleteResponsesSession(key string) error {
	_, err := db.conn.Exec(`DELETE FROM responses_sessions WHERE conversation_id=?`, key)
	return err
}

func (db *DB) cleanResponsesSessionsAt(now time.Time) error {
	last := db.responsesSessionCleanupAt.Load()
	stamp := now.Unix()
	if stamp-last < 60 || !db.responsesSessionCleanupAt.CompareAndSwap(last, stamp) {
		return nil
	}
	// 每分钟最多删除 256 项，避免单连接 SQLite 长时间占用。
	_, err := db.conn.Exec(`DELETE FROM responses_sessions WHERE conversation_id IN
 (SELECT conversation_id FROM responses_sessions WHERE updated_at < ? ORDER BY updated_at LIMIT 256)`,
		now.Add(-ResponsesSessionTTL).Format(time.RFC3339))
	if err != nil {
		db.responsesSessionCleanupAt.CompareAndSwap(stamp, last)
	}
	return err
}

func (db *DB) CleanExpiredResponsesSessions() {
	if err := db.cleanResponsesSessionsAt(time.Now()); err != nil {
		log.Printf("[store] 清理 Responses 状态失败: %v", err)
	}
}
