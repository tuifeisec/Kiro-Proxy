package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ModelGroupMember struct {
	ModelID        string `json:"model_id"`
	Priority       int    `json:"priority"`
	MaxConcurrency int    `json:"max_concurrency"`
}

type ModelGroup struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	Description        string             `json:"description"`
	Enabled            bool               `json:"enabled"`
	FirstByteTimeoutMS int                `json:"first_byte_timeout_ms"`
	MaxAttempts        int                `json:"max_attempts"`
	FallbackGroupID    string             `json:"fallback_group_id"`
	ReasoningEffort    string             `json:"reasoning_effort"`
	Members            []ModelGroupMember `json:"members"`
	CreatedAt          string             `json:"created_at"`
	UpdatedAt          string             `json:"updated_at"`
}

type ModelHealthStatus struct {
	ModelConfigID       string `json:"model_config_id"`
	State               string `json:"state"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	OpenUntil           string `json:"open_until,omitempty"`
	AffinitySessions    int    `json:"affinity_sessions"`
	// ActiveRequests is retained for clients that still consume the old field.
	ActiveRequests       int    `json:"active_requests"`
	CurrentConcurrency   int    `json:"current_concurrency"`
	MaxConcurrency       int    `json:"max_concurrency"`
	AvoidedScopes        int    `json:"avoided_scopes"`
	FirstByteTimeoutMS   int    `json:"first_byte_timeout_ms"`
	LastFirstByteMS      int64  `json:"last_first_byte_ms"`
	AverageFirstByteMS   int64  `json:"average_first_byte_ms"`
	EWMAFirstByteMS      int64  `json:"ewma_first_byte_ms"`
	P95FirstByteMS       int64  `json:"p95_first_byte_ms"`
	FirstByteSampleCount int64  `json:"first_byte_sample_count"`
	SlowFirstByteSamples int    `json:"slow_first_byte_samples"`
	LatencyDegraded      bool   `json:"latency_degraded"`
	LatencyDegradedUntil string `json:"latency_degraded_until,omitempty"`
	UpstreamBucketState  string `json:"upstream_bucket_state,omitempty"`
}

func (db *DB) GetModelGroups() ([]ModelGroup, error) {
	rows, err := db.conn.Query(`
		SELECT id, name, description, enabled, first_byte_timeout_ms, max_attempts,
		       fallback_group_id, reasoning_effort, created_at, updated_at
		FROM model_groups
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := make([]ModelGroup, 0)
	byID := make(map[string]int)
	for rows.Next() {
		var group ModelGroup
		var enabled int
		if err := rows.Scan(&group.ID, &group.Name, &group.Description, &enabled, &group.FirstByteTimeoutMS,
			&group.MaxAttempts, &group.FallbackGroupID, &group.ReasoningEffort, &group.CreatedAt, &group.UpdatedAt); err != nil {
			return nil, err
		}
		group.Enabled = enabled == 1
		group.Members = []ModelGroupMember{}
		byID[group.ID] = len(groups)
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	memberRows, err := db.conn.Query(`
		SELECT group_id, model_id, priority, max_concurrency
		FROM model_group_members
		ORDER BY group_id ASC, priority ASC
	`)
	if err != nil {
		return nil, err
	}
	defer memberRows.Close()
	for memberRows.Next() {
		var groupID string
		var member ModelGroupMember
		if err := memberRows.Scan(&groupID, &member.ModelID, &member.Priority, &member.MaxConcurrency); err != nil {
			return nil, err
		}
		if index, ok := byID[groupID]; ok {
			groups[index].Members = append(groups[index].Members, member)
		}
	}
	return groups, memberRows.Err()
}

func (db *DB) GetModelGroup(id string) (*ModelGroup, error) {
	groups, err := db.GetModelGroups()
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].ID == id {
			return &groups[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (db *DB) FindModelGroupByName(name string) (*ModelGroup, error) {
	name = strings.TrimSpace(name)
	var group ModelGroup
	var enabled int
	err := db.conn.QueryRow(`
		SELECT id, name, description, enabled, first_byte_timeout_ms, max_attempts,
		       fallback_group_id, reasoning_effort, created_at, updated_at
		FROM model_groups
		WHERE name = ? COLLATE NOCASE
		LIMIT 1
	`, name).Scan(&group.ID, &group.Name, &group.Description, &enabled, &group.FirstByteTimeoutMS,
		&group.MaxAttempts, &group.FallbackGroupID, &group.ReasoningEffort, &group.CreatedAt, &group.UpdatedAt)
	if err != nil {
		return nil, err
	}
	group.Enabled = enabled == 1
	return &group, nil
}

func (db *DB) CreateModelGroup(group *ModelGroup) error {
	if group == nil {
		return fmt.Errorf("model group is required")
	}
	if group.ID == "" {
		group.ID = uuid.New().String()
	}
	now := time.Now().UTC().Format(time.RFC3339)
	group.CreatedAt = now
	group.UpdatedAt = now
	return db.saveModelGroup(group, true)
}

func (db *DB) UpdateModelGroup(group *ModelGroup) error {
	if group == nil || strings.TrimSpace(group.ID) == "" {
		return fmt.Errorf("model group id is required")
	}
	group.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return db.saveModelGroup(group, false)
}

func (db *DB) saveModelGroup(group *ModelGroup, create bool) error {
	group.Name = strings.TrimSpace(group.Name)
	if group.Name == "" {
		return fmt.Errorf("model group name is required")
	}
	group.Description = strings.TrimSpace(group.Description)
	if len([]rune(group.Description)) > 500 {
		return fmt.Errorf("model group description must not exceed 500 characters")
	}
	if group.FirstByteTimeoutMS == 0 {
		group.FirstByteTimeoutMS = 10000
	}
	if group.FirstByteTimeoutMS < 10000 || group.FirstByteTimeoutMS > 300000 {
		return fmt.Errorf("first byte timeout must be between 10000 and 300000 milliseconds")
	}
	if group.MaxAttempts == 0 {
		group.MaxAttempts = 4
	}
	if group.MaxAttempts < 1 || group.MaxAttempts > 16 {
		return fmt.Errorf("max attempts must be between 1 and 16")
	}
	group.FallbackGroupID = strings.TrimSpace(group.FallbackGroupID)
	group.ReasoningEffort = strings.ToLower(strings.TrimSpace(group.ReasoningEffort))
	switch group.ReasoningEffort {
	case "", "none", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("unsupported group reasoning effort %q", group.ReasoningEffort)
	}
	if group.FallbackGroupID == group.ID {
		return fmt.Errorf("fallback group cannot reference itself")
	}
	members, err := normalizeModelGroupMembers(group.Members)
	if err != nil {
		return err
	}
	group.Members = members

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := validateModelGroupMembers(tx, group.ID, members); err != nil {
		return err
	}
	if err := validateFallbackGroup(tx, group.ID, group.FallbackGroupID); err != nil {
		return err
	}
	enabled := boolToInt(group.Enabled)
	if create {
		_, err = tx.Exec(`
			INSERT INTO model_groups (
				id, name, description, enabled, first_byte_timeout_ms, max_attempts,
				fallback_group_id, reasoning_effort, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, group.ID, group.Name, group.Description, enabled, group.FirstByteTimeoutMS, group.MaxAttempts,
			group.FallbackGroupID, group.ReasoningEffort, group.CreatedAt, group.UpdatedAt)
	} else {
		var result sql.Result
		result, err = tx.Exec(`
			UPDATE model_groups
			SET name = ?, description = ?, enabled = ?, first_byte_timeout_ms = ?, max_attempts = ?,
			    fallback_group_id = ?, reasoning_effort = ?, updated_at = ?
			WHERE id = ?
		`, group.Name, group.Description, enabled, group.FirstByteTimeoutMS, group.MaxAttempts,
			group.FallbackGroupID, group.ReasoningEffort, group.UpdatedAt, group.ID)
		if err == nil {
			if count, rowsErr := result.RowsAffected(); rowsErr == nil && count == 0 {
				return fmt.Errorf("model group not found: %s", group.ID)
			}
		}
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM model_group_members WHERE group_id = ?`, group.ID); err != nil {
		return err
	}
	for _, member := range members {
		if _, err := tx.Exec(`
			INSERT INTO model_group_members (group_id, model_id, priority, max_concurrency)
			VALUES (?, ?, ?, ?)
		`, group.ID, member.ModelID, member.Priority, member.MaxConcurrency); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func normalizeModelGroupMembers(members []ModelGroupMember) ([]ModelGroupMember, error) {
	result := make([]ModelGroupMember, 0, len(members))
	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		member.ModelID = strings.TrimSpace(member.ModelID)
		if member.ModelID == "" {
			return nil, fmt.Errorf("model group member id is required")
		}
		if _, exists := seen[member.ModelID]; exists {
			return nil, fmt.Errorf("duplicate model group member: %s", member.ModelID)
		}
		seen[member.ModelID] = struct{}{}
		if member.MaxConcurrency < 0 || member.MaxConcurrency > 1000 {
			return nil, fmt.Errorf("max concurrency must be between 0 and 1000")
		}
		member.Priority = len(result)
		result = append(result, member)
	}
	return result, nil
}

func validateFallbackGroup(tx *sql.Tx, groupID, fallbackGroupID string) error {
	if fallbackGroupID == "" {
		return nil
	}
	seen := map[string]bool{groupID: true}
	current := fallbackGroupID
	for current != "" {
		if seen[current] {
			return fmt.Errorf("fallback model groups cannot form a cycle")
		}
		seen[current] = true
		var next string
		if err := tx.QueryRow(`
			SELECT fallback_group_id FROM model_groups WHERE id = ?
		`, current).Scan(&next); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("fallback model group not found: %s", current)
			}
			return err
		}
		current = strings.TrimSpace(next)
	}
	return nil
}

func validateModelGroupMembers(tx *sql.Tx, groupID string, members []ModelGroupMember) error {
	for _, member := range members {
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM models WHERE id = ?`, member.ModelID).Scan(&exists); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("model not found: %s", member.ModelID)
			}
			return err
		}
		var existingGroup string
		err := tx.QueryRow(`
			SELECT group_id FROM model_group_members WHERE model_id = ? AND group_id <> ?
		`, member.ModelID, groupID).Scan(&existingGroup)
		if err == nil {
			return fmt.Errorf("model %s already belongs to group %s", member.ModelID, existingGroup)
		}
		if err != sql.ErrNoRows {
			return err
		}
	}
	return nil
}

func (db *DB) DeleteModelGroup(id string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE model_groups SET fallback_group_id = '', updated_at = ?
		WHERE fallback_group_id = ?
	`, time.Now().UTC().Format(time.RFC3339), id); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM model_groups WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if count, rowsErr := result.RowsAffected(); rowsErr == nil && count == 0 {
		return fmt.Errorf("model group not found: %s", id)
	}
	return tx.Commit()
}
