package store

import (
	"fmt"
	"time"

	json "github.com/goccy/go-json"
)

// ReasoningTrace is provider-issued reasoning metadata for one assistant
// tool-call turn. It is scoped by conversation branch and model configuration.
type ReasoningTrace struct {
	StateKey  string
	TurnKey   string
	Protocol  string
	Text      string
	Signature string
	ItemID    string
	Status    string
	WireField string
	// Blocks keeps Anthropic thinking/redacted_thinking blocks in upstream
	// order, each with its own signature. Rows written before blocks_json
	// existed leave it empty and are read back through Text/Signature.
	Blocks    []ReasoningTraceBlock
	UpdatedAt time.Time
}

// ReasoningTraceBlock is the persisted form of one reasoning block (blocks_json).
type ReasoningTraceBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

func (db *DB) UpsertReasoningTrace(trace *ReasoningTrace) error {
	blocksJSON := ""
	if len(trace.Blocks) > 0 {
		encoded, err := json.Marshal(trace.Blocks)
		if err != nil {
			return fmt.Errorf("编码推理块失败: %w", err)
		}
		blocksJSON = string(encoded)
	}
	_, err := db.conn.Exec(`
		INSERT INTO reasoning_traces (
			state_key, turn_key, protocol, text, signature, item_id, status, wire_field, blocks_json, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(state_key, turn_key) DO UPDATE SET
			protocol = excluded.protocol,
			text = excluded.text,
			signature = excluded.signature,
			item_id = excluded.item_id,
			status = excluded.status,
			wire_field = excluded.wire_field,
			blocks_json = excluded.blocks_json,
			updated_at = excluded.updated_at
	`, trace.StateKey, trace.TurnKey, trace.Protocol, trace.Text, trace.Signature,
		trace.ItemID, trace.Status, trace.WireField, blocksJSON, trace.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func (db *DB) ListReasoningTraces(stateKey string) ([]ReasoningTrace, error) {
	rows, err := db.conn.Query(`
		SELECT state_key, turn_key, protocol, text, signature, item_id, status, wire_field, blocks_json, updated_at
		FROM reasoning_traces WHERE state_key = ? ORDER BY updated_at ASC
	`, stateKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ReasoningTrace
	for rows.Next() {
		var trace ReasoningTrace
		var blocksJSON, updatedAt string
		if err := rows.Scan(&trace.StateKey, &trace.TurnKey, &trace.Protocol, &trace.Text,
			&trace.Signature, &trace.ItemID, &trace.Status, &trace.WireField, &blocksJSON, &updatedAt); err != nil {
			return nil, err
		}
		if blocksJSON != "" {
			// 只由 UpsertReasoningTrace 写入；解析失败说明数据损坏，直接报错而不是丢掉推理静默回放
			if err := json.Unmarshal([]byte(blocksJSON), &trace.Blocks); err != nil {
				return nil, fmt.Errorf("解析推理块失败 turn=%s: %w", trace.TurnKey, err)
			}
		}
		trace.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		result = append(result, trace)
	}
	return result, rows.Err()
}

func (db *DB) CleanExpiredReasoningTraces() {
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, _ = db.conn.Exec(`DELETE FROM reasoning_traces WHERE updated_at < ?`, cutoff)
}
