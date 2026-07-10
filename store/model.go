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
	json "github.com/goccy/go-json"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Model 一条模型配置
type Model struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`                // 显示名称（如 "OpenAI - GPT-4o"）
	ModelID              string  `json:"model_id"`            // 模型标识（如 "gpt-4o"），发给上游的 model 值
	Provider             string  `json:"provider"`            // openai | anthropic，决定格式转换
	BaseURL              string  `json:"base_url"`            // 接口地址（如 "https://api.openai.com"，不会自动追加版本路径）
	APIKey               string  `json:"api_key"`             // 访问密钥；codex 模式下为自动刷新的 access_token
	RefreshToken         string  `json:"refresh_token"`       // codex OAuth refresh_token
	ProxyURL             string  `json:"proxy_url"`           // 单模型上游代理地址，留空则直连
	Endpoint             string  `json:"endpoint"`            // 接口端点路径（如 chat/completions，需完整填写）
	DisableAutoSuffix    bool    `json:"disable_auto_suffix"` // 不自动改写端点或追加 query，严格使用用户配置的接口路径
	ContextWindow        int     `json:"context_window"`      // 上下文窗口，0=默认
	MaxTokens            int     `json:"max_tokens"`          // 最大输出 Token，0=默认
	ReasoningEffort      string  `json:"reasoning_effort"`    // 推理强度：空/low/medium/high
	RateMultiplier       float64 `json:"rate_multiplier"`     // 兼容旧字段：倍率（用于 Kiro credit 计算），默认 1.0
	InputPrice           float64 `json:"input_price"`         // 普通输入价格（每百万 token）
	OutputPrice          float64 `json:"output_price"`        // 普通输出价格（每百万 token）
	CacheInputPrice      float64 `json:"cache_input_price"`   // 缓存命中/读取输入价格（每百万 token）
	CacheOutputPrice     float64 `json:"cache_output_price"`  // 缓存写入价格（每百万 token）
	EnableContinuation   bool    `json:"enable_continuation"` // 启用续传（previous_response_id），默认关闭
	CodexEmail           string  `json:"codex_email"`
	CodexPlanType        string  `json:"codex_plan_type"`
	CodexAccountID       string  `json:"codex_account_id"`
	AccessTokenExpiresAt int64   `json:"access_token_expires_at"`
	CodexUsageJSON       string  `json:"codex_usage_json"`
	ServiceTier          string  `json:"service_tier"` // Codex 速度层：空=标准，fast=快速
	Enabled              bool    `json:"enabled"`
	SortOrder            int     `json:"sort_order"`
	Note                 string  `json:"note"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
}

func (db *DB) GetModels() ([]Model, error) {
	rows, err := db.conn.Query(`
		SELECT id, name, model_id, provider, base_url, api_key, refresh_token, proxy_url,
		       endpoint, disable_auto_suffix, context_window, max_tokens, reasoning_effort,
		       rate_multiplier, input_price, output_price, cache_input_price, cache_output_price,
		       enable_continuation, codex_email, codex_plan_type, codex_account_id, access_token_expires_at, codex_usage_json,
		       service_tier, enabled, sort_order, note, created_at, updated_at
		FROM models ORDER BY sort_order ASC, created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []Model
	for rows.Next() {
		var m Model
		var enabled, enableCont, disableAuto int
		if err := rows.Scan(&m.ID, &m.Name, &m.ModelID,
			&m.Provider, &m.BaseURL, &m.APIKey, &m.RefreshToken, &m.ProxyURL, &m.Endpoint, &disableAuto,
			&m.ContextWindow, &m.MaxTokens, &m.ReasoningEffort,
			&m.RateMultiplier, &m.InputPrice, &m.OutputPrice, &m.CacheInputPrice, &m.CacheOutputPrice,
			&enableCont, &m.CodexEmail, &m.CodexPlanType, &m.CodexAccountID, &m.AccessTokenExpiresAt, &m.CodexUsageJSON,
			&m.ServiceTier, &enabled, &m.SortOrder, &m.Note, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.Enabled = enabled == 1
		m.EnableContinuation = enableCont == 1
		m.DisableAutoSuffix = disableAuto == 1
		models = append(models, m)
	}
	return models, rows.Err()
}

func (db *DB) GetModel(id string) (*Model, error) {
	var m Model
	var enabled, enableCont, disableAuto int
	err := db.conn.QueryRow(`
		SELECT id, name, model_id, provider, base_url, api_key, refresh_token, proxy_url,
		       endpoint, disable_auto_suffix, context_window, max_tokens, reasoning_effort,
		       rate_multiplier, input_price, output_price, cache_input_price, cache_output_price,
		       enable_continuation, codex_email, codex_plan_type, codex_account_id, access_token_expires_at, codex_usage_json,
		       service_tier, enabled, sort_order, note, created_at, updated_at
		FROM models WHERE id = ?
	`, id).Scan(&m.ID, &m.Name, &m.ModelID,
		&m.Provider, &m.BaseURL, &m.APIKey, &m.RefreshToken, &m.ProxyURL, &m.Endpoint, &disableAuto,
		&m.ContextWindow, &m.MaxTokens, &m.ReasoningEffort,
		&m.RateMultiplier, &m.InputPrice, &m.OutputPrice, &m.CacheInputPrice, &m.CacheOutputPrice,
		&enableCont, &m.CodexEmail, &m.CodexPlanType, &m.CodexAccountID, &m.AccessTokenExpiresAt, &m.CodexUsageJSON,
		&m.ServiceTier, &enabled, &m.SortOrder, &m.Note, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	m.Enabled = enabled == 1
	m.EnableContinuation = enableCont == 1
	m.DisableAutoSuffix = disableAuto == 1
	return &m, nil
}

// FindByModelID 根据 modelId 查找模型配置（路由用）
func (db *DB) FindByModelID(modelID string) (*Model, error) {
	var m Model
	var enabled, enableCont, disableAuto int
	err := db.conn.QueryRow(`
		SELECT id, name, model_id, provider, base_url, api_key, refresh_token, proxy_url,
		       endpoint, disable_auto_suffix, context_window, max_tokens, reasoning_effort,
		       rate_multiplier, input_price, output_price, cache_input_price, cache_output_price,
		       enable_continuation, codex_email, codex_plan_type, codex_account_id, access_token_expires_at, codex_usage_json,
		       service_tier, enabled, sort_order, note, created_at, updated_at
		FROM models WHERE model_id = ? AND enabled = 1 LIMIT 1
	`, modelID).Scan(&m.ID, &m.Name, &m.ModelID,
		&m.Provider, &m.BaseURL, &m.APIKey, &m.RefreshToken, &m.ProxyURL, &m.Endpoint, &disableAuto,
		&m.ContextWindow, &m.MaxTokens, &m.ReasoningEffort,
		&m.RateMultiplier, &m.InputPrice, &m.OutputPrice, &m.CacheInputPrice, &m.CacheOutputPrice,
		&enableCont, &m.CodexEmail, &m.CodexPlanType, &m.CodexAccountID, &m.AccessTokenExpiresAt, &m.CodexUsageJSON,
		&m.ServiceTier, &enabled, &m.SortOrder, &m.Note, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	m.Enabled = true
	m.EnableContinuation = enableCont == 1
	m.DisableAutoSuffix = disableAuto == 1
	return &m, nil
}

// FindModelByName 根据显示名称查找模型（用于唯一性校验）
func (db *DB) FindModelByName(name string) (*Model, error) {
	var m Model
	var enabled, enableCont, disableAuto int
	err := db.conn.QueryRow(`
		SELECT id, name, model_id, provider, base_url, api_key, refresh_token, proxy_url,
		       endpoint, disable_auto_suffix, context_window, max_tokens, reasoning_effort,
		       rate_multiplier, input_price, output_price, cache_input_price, cache_output_price,
		       enable_continuation, codex_email, codex_plan_type, codex_account_id, access_token_expires_at, codex_usage_json,
		       service_tier, enabled, sort_order, note, created_at, updated_at
		FROM models WHERE name = ? LIMIT 1
	`, name).Scan(&m.ID, &m.Name, &m.ModelID,
		&m.Provider, &m.BaseURL, &m.APIKey, &m.RefreshToken, &m.ProxyURL, &m.Endpoint, &disableAuto,
		&m.ContextWindow, &m.MaxTokens, &m.ReasoningEffort,
		&m.RateMultiplier, &m.InputPrice, &m.OutputPrice, &m.CacheInputPrice, &m.CacheOutputPrice,
		&enableCont, &m.CodexEmail, &m.CodexPlanType, &m.CodexAccountID, &m.AccessTokenExpiresAt, &m.CodexUsageJSON,
		&m.ServiceTier, &enabled, &m.SortOrder, &m.Note, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	m.Enabled = enabled == 1
	m.EnableContinuation = enableCont == 1
	m.DisableAutoSuffix = disableAuto == 1
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
	m.ProxyURL = strings.TrimSpace(m.ProxyURL)
	m.ServiceTier = normalizeServiceTier(m.ServiceTier)
	normalizeModelPrices(m)
	enabled := 0
	if m.Enabled {
		enabled = 1
	}
	enableCont := 0
	if m.EnableContinuation {
		enableCont = 1
	}
	_, err := db.conn.Exec(`
		INSERT INTO models (id, name, model_id, provider, base_url, api_key, refresh_token, proxy_url,
		  endpoint, disable_auto_suffix, context_window, max_tokens, reasoning_effort, rate_multiplier,
		  input_price, output_price, cache_input_price, cache_output_price,
		  enable_continuation, codex_email, codex_plan_type, codex_account_id, access_token_expires_at, codex_usage_json,
		  service_tier, enabled, sort_order, note, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.ID, m.Name, m.ModelID, m.Provider, m.BaseURL, m.APIKey, m.RefreshToken, m.ProxyURL,
		m.Endpoint, boolToInt(m.DisableAutoSuffix), m.ContextWindow, m.MaxTokens, m.ReasoningEffort,
		m.RateMultiplier, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice,
		enableCont, m.CodexEmail, m.CodexPlanType, m.CodexAccountID, m.AccessTokenExpiresAt, m.CodexUsageJSON,
		m.ServiceTier, enabled, m.SortOrder, m.Note, m.CreatedAt, m.UpdatedAt)
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
	m.ProxyURL = strings.TrimSpace(m.ProxyURL)
	m.ServiceTier = normalizeServiceTier(m.ServiceTier)
	normalizeModelPrices(m)
	res, err := db.conn.Exec(`
		UPDATE models SET name=?, model_id=?, provider=?, base_url=?, api_key=?, refresh_token=?, proxy_url=?,
		  endpoint=?, disable_auto_suffix=?, context_window=?, max_tokens=?, reasoning_effort=?,
		  rate_multiplier=?, input_price=?, output_price=?, cache_input_price=?, cache_output_price=?,
		  enable_continuation=?, codex_email=?, codex_plan_type=?, codex_account_id=?, access_token_expires_at=?, codex_usage_json=?,
		  service_tier=?, enabled=?, sort_order=?, note=?, updated_at=?
		WHERE id=?
	`, m.Name, m.ModelID, m.Provider, m.BaseURL, m.APIKey, m.RefreshToken, m.ProxyURL,
		m.Endpoint, boolToInt(m.DisableAutoSuffix), m.ContextWindow, m.MaxTokens, m.ReasoningEffort,
		m.RateMultiplier, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice,
		enableCont, m.CodexEmail, m.CodexPlanType, m.CodexAccountID, m.AccessTokenExpiresAt, m.CodexUsageJSON,
		m.ServiceTier, enabled, m.SortOrder, m.Note, m.UpdatedAt, m.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("model not found: %s", m.ID)
	}
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (db *DB) DeleteModel(id string) error {
	_, err := db.conn.Exec(`DELETE FROM models WHERE id = ?`, id)
	return err
}

func normalizeServiceTier(serviceTier string) string {
	serviceTier = strings.ToLower(strings.TrimSpace(serviceTier))
	if serviceTier == "fast" {
		return "fast"
	}
	return ""
}

func normalizeModelPrices(m *Model) {
	if m.RateMultiplier <= 0 {
		m.RateMultiplier = 1.0
	}
	defaults := defaultPricesForProvider(m.Provider)
	if m.InputPrice <= 0 {
		m.InputPrice = defaults.input
	}
	if m.OutputPrice <= 0 {
		m.OutputPrice = defaults.output
	}
	if m.CacheInputPrice <= 0 {
		m.CacheInputPrice = defaults.cacheInput
	}
	if m.CacheOutputPrice <= 0 {
		m.CacheOutputPrice = defaults.cacheOutput
	}
}

type defaultModelPrices struct {
	input       float64
	output      float64
	cacheInput  float64
	cacheOutput float64
}

func defaultPricesForProvider(provider string) defaultModelPrices {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return defaultModelPrices{input: 5, output: 25, cacheInput: 0.5, cacheOutput: 6.25}
	case "gemini":
		return defaultModelPrices{input: 1.5, output: 9, cacheInput: 0.15, cacheOutput: 1.5}
	default:
		return defaultModelPrices{input: 5, output: 30, cacheInput: 0.5, cacheOutput: 30}
	}
}

// CalculateCredits 按每百万 token 的模型价格计算 Credit 用量。
func CalculateCredits(inputTokens, outputTokens, cacheRead, cacheWrite int, m *Model) float64 {
	if m == nil {
		m = &Model{}
	}
	cp := *m
	normalizeModelPrices(&cp)
	if cacheRead < 0 {
		cacheRead = 0
	}
	if cacheWrite < 0 {
		cacheWrite = 0
	}
	regularInput := inputTokens - cacheRead - cacheWrite
	if regularInput < 0 {
		regularInput = 0
	}
	return ((float64(regularInput)*cp.InputPrice +
		float64(outputTokens)*cp.OutputPrice +
		float64(cacheRead)*cp.CacheInputPrice +
		float64(cacheWrite)*cp.CacheOutputPrice) / 1000000) * cp.RateMultiplier
}

func (db *DB) UpdateCodexTokenAndUsage(m *Model) error {
	if m == nil || m.ID == "" {
		return fmt.Errorf("model is required")
	}
	_, err := db.conn.Exec(`
		UPDATE models SET api_key=?, refresh_token=?, codex_email=?, codex_plan_type=?, codex_account_id=?,
		  access_token_expires_at=?, codex_usage_json=?, updated_at=?
		WHERE id=?
	`, m.APIKey, m.RefreshToken, m.CodexEmail, m.CodexPlanType, m.CodexAccountID,
		m.AccessTokenExpiresAt, m.CodexUsageJSON, time.Now().UTC().Format(time.RFC3339), m.ID)
	return err
}

func (m *Model) CodexUsage() map[string]interface{} {
	if m == nil || strings.TrimSpace(m.CodexUsageJSON) == "" {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(m.CodexUsageJSON), &out); err != nil {
		return nil
	}
	return out
}
