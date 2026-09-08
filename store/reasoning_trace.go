package store

import "time"

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
	UpdatedAt time.Time
}

func (db *DB) UpsertReasoningTrace(trace *ReasoningTrace) error {
	_, err := db.conn.Exec(`
		INSERT INTO reasoning_traces (
			state_key, turn_key, protocol, text, signature, item_id, status, wire_field, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(state_key, turn_key) DO UPDATE SET
			protocol = excluded.protocol,
			text = excluded.text,
			signature = excluded.signature,
			item_id = excluded.item_id,
			status = excluded.status,
			wire_field = excluded.wire_field,
			updated_at = excluded.updated_at
	`, trace.StateKey, trace.TurnKey, trace.Protocol, trace.Text, trace.Signature,
		trace.ItemID, trace.Status, trace.WireField, trace.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func (db *DB) ListReasoningTraces(stateKey string) ([]ReasoningTrace, error) {
	rows, err := db.conn.Query(`
		SELECT state_key, turn_key, protocol, text, signature, item_id, status, wire_field, updated_at
		FROM reasoning_traces WHERE state_key = ? ORDER BY updated_at ASC
	`, stateKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ReasoningTrace
	for rows.Next() {
		var trace ReasoningTrace
		var updatedAt string
		if err := rows.Scan(&trace.StateKey, &trace.TurnKey, &trace.Protocol, &trace.Text,
			&trace.Signature, &trace.ItemID, &trace.Status, &trace.WireField, &updatedAt); err != nil {
			return nil, err
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
