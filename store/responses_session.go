// responses_session.go
// Responses API 会话状态存储：用于 previous_response_id 续传机制
//
// 工作原理：
//   1. 每次 Responses API 请求完成后，保存 response_id + input 快照
//   2. 下次同一会话请求时，对比 input 前缀是否一致
//   3. 如果前缀一致，则使用 previous_response_id + delta input 替代全量重发
//   4. 这样上游无需重新匹配缓存前缀，直接续传，命中率接近 100%

package store

import (
	"database/sql"
	"time"
)

// ResponsesSession 存储一次成功的 Responses API 交互状态
type ResponsesSession struct {
	StateKey   string // continuation 状态键（沿用 conversation_id 数据库列）
	InputHash  string // 本次 input 数组的 sha256 前缀 hash（用于快速比对）
	ResponseID string // 上游返回的 response.id
	InputJSON  string // 本次完整 input 数组的 JSON（用于 delta 计算）
	ItemCount  int    // input 数组的 item 数量
	UpdatedAt  time.Time
}

// UpsertResponsesSession 保存或更新 Responses 会话状态
func (db *DB) UpsertResponsesSession(session *ResponsesSession) error {
	_, err := db.conn.Exec(`
		INSERT INTO responses_sessions (conversation_id, input_hash, response_id, input_json, item_count, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(conversation_id) DO UPDATE SET
			input_hash  = excluded.input_hash,
			response_id = excluded.response_id,
			input_json  = excluded.input_json,
			item_count  = excluded.item_count,
			updated_at  = excluded.updated_at
	`, session.StateKey, session.InputHash, session.ResponseID, session.InputJSON, session.ItemCount, session.UpdatedAt.Format(time.RFC3339))
	return err
}

// GetResponsesSession 获取指定会话的上一次状态
func (db *DB) GetResponsesSession(stateKey string) (*ResponsesSession, error) {
	row := db.conn.QueryRow(`
		SELECT conversation_id, input_hash, response_id, input_json, item_count, updated_at
		FROM responses_sessions
		WHERE conversation_id = ?
	`, stateKey)

	var s ResponsesSession
	var updatedAt string
	err := row.Scan(&s.StateKey, &s.InputHash, &s.ResponseID, &s.InputJSON, &s.ItemCount, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &s, nil
}

// DeleteResponsesSession 删除指定会话的状态（用于续传失败时清理）
func (db *DB) DeleteResponsesSession(stateKey string) error {
	_, err := db.conn.Exec(`DELETE FROM responses_sessions WHERE conversation_id = ?`, stateKey)
	return err
}

// CleanExpiredResponsesSessions 清理过期的会话状态（7 天未更新的）
func (db *DB) CleanExpiredResponsesSessions() {
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	db.conn.Exec(`DELETE FROM responses_sessions WHERE updated_at < ?`, cutoff)
}
