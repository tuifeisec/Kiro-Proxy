// model.go
// 模型配置：一条记录 = 一个可用模型（Kiro 模型列表里的一项）
//
// 核心字段：
//   name:      下拉显示名称，Kiro 下拉框里看到的名字，也是 Kiro 发送请求时携带的 modelId
//   model_id:  模型标识，发给上游 API 的 model 字段值（如 "gpt-5.5"）
//   base_url:  上游 API 基础地址（如 https://api.openai.com，不会自动追加版本路径）
//   endpoint:  上游 API 端点路径（如 chat/completions，需由用户完整填写）
//   api_key:   上游 API 密钥
//   provider:  决定格式转换方向（openai / anthropic）
//
// 路由逻辑：Kiro 发来的 modelId 就是 name，路由器按 name 匹配到对应配置，
// 再把真正的 model_id 发给上游。这样多个渠道可使用相同的 model_id 而不冲突。

package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Model 一条模型配置
type Model struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`                // 显示名称（如 "OpenAI - GPT-4o"）
	ModelID            string  `json:"model_id"`            // 模型标识（如 "gpt-4o"），发给上游的 model 值
	Provider           string  `json:"provider"`            // openai | anthropic，决定格式转换
	BaseURL            string  `json:"base_url"`            // 接口地址（如 "https://api.openai.com"，不会自动追加版本路径）
	APIKey             string  `json:"api_key"`             // 访问密钥
	Endpoint           string  `json:"endpoint"`            // 接口端点路径（如 chat/completions，需完整填写）
	ContextWindow      int     `json:"context_window"`      // 上下文窗口，0=默认
	MaxTokens          int     `json:"max_tokens"`          // 最大输出 Token，0=默认
	ReasoningEffort    string  `json:"reasoning_effort"`    // 推理强度：空/low/medium/high
	RateMultiplier     float64 `json:"rate_multiplier"`     // 倍率（用于 Kiro credit 计算），默认 1.0
	EnableContinuation bool    `json:"enable_continuation"` // 启用续传（previous_response_id），默认关闭
	Enabled            bool    `json:"enabled"`
	SortOrder          int     `json:"sort_order"`
	Note               string  `json:"note"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

func (db *DB) GetModels() ([]Model, error) {
	rows, err := db.conn.Query(`
		SELECT id, name, model_id, provider, base_url, api_key,
		       endpoint, context_window, max_tokens, reasoning_effort,
		       rate_multiplier, enable_continuation, enabled, sort_order, note, created_at, updated_at
		FROM models ORDER BY sort_order ASC, created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []Model
	for rows.Next() {
		var m Model
		var enabled, enableCont int
		if err := rows.Scan(&m.ID, &m.Name, &m.ModelID,
			&m.Provider, &m.BaseURL, &m.APIKey, &m.Endpoint,
			&m.ContextWindow, &m.MaxTokens, &m.ReasoningEffort,
			&m.RateMultiplier, &enableCont, &enabled, &m.SortOrder, &m.Note,
			&m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.Enabled = enabled == 1
		m.EnableContinuation = enableCont == 1
		models = append(models, m)
	}
	return models, rows.Err()
}

func (db *DB) GetModel(id string) (*Model, error) {
	var m Model
	var enabled, enableCont int
	err := db.conn.QueryRow(`
		SELECT id, name, model_id, provider, base_url, api_key,
		       endpoint, context_window, max_tokens, reasoning_effort,
		       rate_multiplier, enable_continuation, enabled, sort_order, note, created_at, updated_at
		FROM models WHERE id = ?
	`, id).Scan(&m.ID, &m.Name, &m.ModelID,
		&m.Provider, &m.BaseURL, &m.APIKey, &m.Endpoint,
		&m.ContextWindow, &m.MaxTokens, &m.ReasoningEffort,
		&m.RateMultiplier, &enableCont, &enabled, &m.SortOrder, &m.Note,
		&m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	m.Enabled = enabled == 1
	m.EnableContinuation = enableCont == 1
	return &m, nil
}

// FindByModelID 根据 modelId 查找模型配置（路由用）
func (db *DB) FindByModelID(modelID string) (*Model, error) {
	var m Model
	var enabled, enableCont int
	err := db.conn.QueryRow(`
		SELECT id, name, model_id, provider, base_url, api_key,
		       endpoint, context_window, max_tokens, reasoning_effort,
		       rate_multiplier, enable_continuation, enabled, sort_order, note, created_at, updated_at
		FROM models WHERE model_id = ? AND enabled = 1 LIMIT 1
	`, modelID).Scan(&m.ID, &m.Name, &m.ModelID,
		&m.Provider, &m.BaseURL, &m.APIKey, &m.Endpoint,
		&m.ContextWindow, &m.MaxTokens, &m.ReasoningEffort,
		&m.RateMultiplier, &enableCont, &enabled, &m.SortOrder, &m.Note,
		&m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	m.Enabled = true
	m.EnableContinuation = enableCont == 1
	return &m, nil
}

func (db *DB) CreateModel(m *Model) error {
	if m.ID == "" {
		m.ID = uuid.New().String()
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m.CreatedAt = now
	m.UpdatedAt = now
	if strings.TrimSpace(m.Endpoint) == "" {
		return fmt.Errorf("endpoint is required")
	}
	m.Endpoint = strings.TrimSpace(m.Endpoint)
	if m.RateMultiplier <= 0 {
		m.RateMultiplier = 1.0
	}
	enabled := 0
	if m.Enabled {
		enabled = 1
	}
	enableCont := 0
	if m.EnableContinuation {
		enableCont = 1
	}
	_, err := db.conn.Exec(`
		INSERT INTO models (id, name, model_id, provider, base_url, api_key,
		  endpoint, context_window, max_tokens, reasoning_effort, rate_multiplier,
		  enable_continuation, enabled, sort_order, note, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.ID, m.Name, m.ModelID, m.Provider, m.BaseURL, m.APIKey,
		m.Endpoint, m.ContextWindow, m.MaxTokens, m.ReasoningEffort,
		m.RateMultiplier, enableCont, enabled, m.SortOrder, m.Note, m.CreatedAt, m.UpdatedAt)
	return err
}

func (db *DB) UpdateModel(m *Model) error {
	m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	enabled := 0
	if m.Enabled {
		enabled = 1
	}
	enableCont := 0
	if m.EnableContinuation {
		enableCont = 1
	}
	if strings.TrimSpace(m.Endpoint) == "" {
		return fmt.Errorf("endpoint is required")
	}
	m.Endpoint = strings.TrimSpace(m.Endpoint)
	if m.RateMultiplier <= 0 {
		m.RateMultiplier = 1.0
	}
	res, err := db.conn.Exec(`
		UPDATE models SET name=?, model_id=?, provider=?, base_url=?, api_key=?,
		  endpoint=?, context_window=?, max_tokens=?, reasoning_effort=?,
		  rate_multiplier=?, enable_continuation=?, enabled=?, sort_order=?, note=?, updated_at=?
		WHERE id=?
	`, m.Name, m.ModelID, m.Provider, m.BaseURL, m.APIKey,
		m.Endpoint, m.ContextWindow, m.MaxTokens, m.ReasoningEffort,
		m.RateMultiplier, enableCont, enabled, m.SortOrder, m.Note, m.UpdatedAt, m.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("model not found: %s", m.ID)
	}
	return nil
}

func (db *DB) DeleteModel(id string) error {
	_, err := db.conn.Exec(`DELETE FROM models WHERE id = ?`, id)
	return err
}
