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
	"database/sql"
	"errors"
	"fmt"
	json "github.com/goccy/go-json"
	"strings"
	"time"

	"kiro-proxy/billing"

	"github.com/google/uuid"
)

// Model 一条模型配置
type Model struct {
	AutoRoutingMode       string  `json:"-"` // 仅虚拟 Auto 元数据，不持久化到实际模型。
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`                    // 显示名称（如 "OpenAI - GPT-4o"）
	ModelID               string  `json:"model_id"`                // 模型标识（如 "gpt-4o"），发给上游的 model 值
	Provider              string  `json:"provider"`                // openai | anthropic，决定格式转换
	BaseURL               string  `json:"base_url"`                // 接口地址（如 "https://api.openai.com"，不会自动追加版本路径）
	APIKey                string  `json:"api_key"`                 // 访问密钥；codex 模式下为自动刷新的 access_token
	RefreshToken          string  `json:"refresh_token"`           // codex OAuth refresh_token
	ProxyURL              string  `json:"proxy_url"`               // 单模型上游代理地址，留空则直连
	Endpoint              string  `json:"endpoint"`                // 接口端点路径（如 chat/completions，需完整填写）
	DisableAutoSuffix     bool    `json:"disable_auto_suffix"`     // 不自动改写端点或追加 query，严格使用用户配置的接口路径
	ContextWindow         int     `json:"context_window"`          // 上下文窗口，0=默认
	MaxTokens             int     `json:"max_tokens"`              // 最大输出 Token，0=默认
	ReasoningEffort       string  `json:"reasoning_effort"`        // 推理强度：空/low/medium/high
	ImageInputMode        string  `json:"image_input_mode"`        // 图片输入能力：auto/supported/unsupported
	RateMultiplier        float64 `json:"rate_multiplier"`         // 兼容旧字段：倍率（用于 Kiro credit 计算），默认 1.0
	InputPrice            float64 `json:"input_price"`             // 普通输入价格（每百万 token）
	OutputPrice           float64 `json:"output_price"`            // 普通输出价格（每百万 token）
	CacheInputPrice       float64 `json:"cache_input_price"`       // 缓存命中/读取输入价格（每百万 token）
	CacheOutputPrice      float64 `json:"cache_output_price"`      // 缓存写入价格（每百万 token）
	EnableContinuation    bool    `json:"enable_continuation"`     // 启用续传（previous_response_id），默认关闭
	EnableOpenCodeSession bool    `json:"enable_opencode_session"` // 启用 OpenCode 会话兼容，默认关闭
	CodexEmail            string  `json:"codex_email"`
	CodexPlanType         string  `json:"codex_plan_type"`
	CodexAccountID        string  `json:"codex_account_id"`
	AccessTokenExpiresAt  int64   `json:"access_token_expires_at"`
	CodexUsageJSON        string  `json:"codex_usage_json"`
	ServiceTier           string  `json:"service_tier"` // Codex 速度层：空=标准，fast=快速
	Enabled               bool    `json:"enabled"`
	SortOrder             int     `json:"sort_order"`
	Note                  string  `json:"note"`
	CreatedAt             string  `json:"created_at"`
	UpdatedAt             string  `json:"updated_at"`
	// manager 在内存中规范化 refresh-token auth payload 后，使用这些请求级
	// 字段保留 DB CAS 所需的原始值。
	CodexStoredAPIKey       string `json:"-"`
	CodexStoredRefreshToken string `json:"-"`
	CodexStoredAccountID    string `json:"-"`
	CodexStoredValues       bool   `json:"-"`
}

var ErrCodexCredentialConflict = errors.New("codex credential CAS conflict")

func (db *DB) GetModels() ([]Model, error) {
	rows, err := db.conn.Query(`
		SELECT id, name, model_id, provider, base_url, api_key, refresh_token, proxy_url,
		       endpoint, disable_auto_suffix, context_window, max_tokens, reasoning_effort, image_input_mode,
		       rate_multiplier, input_price, output_price, cache_input_price, cache_output_price,
		       enable_continuation, enable_opencode_session, codex_email, codex_plan_type, codex_account_id, access_token_expires_at, codex_usage_json,
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
		var enabled, enableCont, enableOpenCode, disableAuto int
		if err := rows.Scan(&m.ID, &m.Name, &m.ModelID,
			&m.Provider, &m.BaseURL, &m.APIKey, &m.RefreshToken, &m.ProxyURL, &m.Endpoint, &disableAuto,
			&m.ContextWindow, &m.MaxTokens, &m.ReasoningEffort, &m.ImageInputMode,
			&m.RateMultiplier, &m.InputPrice, &m.OutputPrice, &m.CacheInputPrice, &m.CacheOutputPrice,
			&enableCont, &enableOpenCode, &m.CodexEmail, &m.CodexPlanType, &m.CodexAccountID, &m.AccessTokenExpiresAt, &m.CodexUsageJSON,
			&m.ServiceTier, &enabled, &m.SortOrder, &m.Note, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.Enabled = enabled == 1
		m.EnableContinuation = enableCont == 1
		m.EnableOpenCodeSession = enableOpenCode == 1
		m.DisableAutoSuffix = disableAuto == 1
		models = append(models, m)
	}
	return models, rows.Err()
}

func (db *DB) GetModel(id string) (*Model, error) {
	var m Model
	var enabled, enableCont, enableOpenCode, disableAuto int
	err := db.conn.QueryRow(`
		SELECT id, name, model_id, provider, base_url, api_key, refresh_token, proxy_url,
		       endpoint, disable_auto_suffix, context_window, max_tokens, reasoning_effort, image_input_mode,
		       rate_multiplier, input_price, output_price, cache_input_price, cache_output_price,
		       enable_continuation, enable_opencode_session, codex_email, codex_plan_type, codex_account_id, access_token_expires_at, codex_usage_json,
		       service_tier, enabled, sort_order, note, created_at, updated_at
		FROM models WHERE id = ?
	`, id).Scan(&m.ID, &m.Name, &m.ModelID,
		&m.Provider, &m.BaseURL, &m.APIKey, &m.RefreshToken, &m.ProxyURL, &m.Endpoint, &disableAuto,
		&m.ContextWindow, &m.MaxTokens, &m.ReasoningEffort, &m.ImageInputMode,
		&m.RateMultiplier, &m.InputPrice, &m.OutputPrice, &m.CacheInputPrice, &m.CacheOutputPrice,
		&enableCont, &enableOpenCode, &m.CodexEmail, &m.CodexPlanType, &m.CodexAccountID, &m.AccessTokenExpiresAt, &m.CodexUsageJSON,
		&m.ServiceTier, &enabled, &m.SortOrder, &m.Note, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	m.Enabled = enabled == 1
	m.EnableContinuation = enableCont == 1
	m.EnableOpenCodeSession = enableOpenCode == 1
	m.DisableAutoSuffix = disableAuto == 1
	return &m, nil
}

// FindByModelID 根据 modelId 查找模型配置（路由用）
func (db *DB) FindByModelID(modelID string) (*Model, error) {
	var m Model
	var enabled, enableCont, enableOpenCode, disableAuto int
	err := db.conn.QueryRow(`
		SELECT id, name, model_id, provider, base_url, api_key, refresh_token, proxy_url,
		       endpoint, disable_auto_suffix, context_window, max_tokens, reasoning_effort, image_input_mode,
		       rate_multiplier, input_price, output_price, cache_input_price, cache_output_price,
		       enable_continuation, enable_opencode_session, codex_email, codex_plan_type, codex_account_id, access_token_expires_at, codex_usage_json,
		       service_tier, enabled, sort_order, note, created_at, updated_at
		FROM models WHERE model_id = ? AND enabled = 1 LIMIT 1
	`, modelID).Scan(&m.ID, &m.Name, &m.ModelID,
		&m.Provider, &m.BaseURL, &m.APIKey, &m.RefreshToken, &m.ProxyURL, &m.Endpoint, &disableAuto,
		&m.ContextWindow, &m.MaxTokens, &m.ReasoningEffort, &m.ImageInputMode,
		&m.RateMultiplier, &m.InputPrice, &m.OutputPrice, &m.CacheInputPrice, &m.CacheOutputPrice,
		&enableCont, &enableOpenCode, &m.CodexEmail, &m.CodexPlanType, &m.CodexAccountID, &m.AccessTokenExpiresAt, &m.CodexUsageJSON,
		&m.ServiceTier, &enabled, &m.SortOrder, &m.Note, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	m.Enabled = true
	m.EnableContinuation = enableCont == 1
	m.EnableOpenCodeSession = enableOpenCode == 1
	m.DisableAutoSuffix = disableAuto == 1
	return &m, nil
}

// FindModelByName 根据显示名称查找模型（用于唯一性校验）
func (db *DB) FindModelByName(name string) (*Model, error) {
	var m Model
	var enabled, enableCont, enableOpenCode, disableAuto int
	err := db.conn.QueryRow(`
		SELECT id, name, model_id, provider, base_url, api_key, refresh_token, proxy_url,
		       endpoint, disable_auto_suffix, context_window, max_tokens, reasoning_effort, image_input_mode,
		       rate_multiplier, input_price, output_price, cache_input_price, cache_output_price,
		       enable_continuation, enable_opencode_session, codex_email, codex_plan_type, codex_account_id, access_token_expires_at, codex_usage_json,
		       service_tier, enabled, sort_order, note, created_at, updated_at
		FROM models WHERE name = ? LIMIT 1
	`, name).Scan(&m.ID, &m.Name, &m.ModelID,
		&m.Provider, &m.BaseURL, &m.APIKey, &m.RefreshToken, &m.ProxyURL, &m.Endpoint, &disableAuto,
		&m.ContextWindow, &m.MaxTokens, &m.ReasoningEffort, &m.ImageInputMode,
		&m.RateMultiplier, &m.InputPrice, &m.OutputPrice, &m.CacheInputPrice, &m.CacheOutputPrice,
		&enableCont, &enableOpenCode, &m.CodexEmail, &m.CodexPlanType, &m.CodexAccountID, &m.AccessTokenExpiresAt, &m.CodexUsageJSON,
		&m.ServiceTier, &enabled, &m.SortOrder, &m.Note, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	m.Enabled = enabled == 1
	m.EnableContinuation = enableCont == 1
	m.EnableOpenCodeSession = enableOpenCode == 1
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
	imageInputMode, err := NormalizeImageInputMode(m.ImageInputMode)
	if err != nil {
		return err
	}
	m.ImageInputMode = imageInputMode
	normalizeModelPrices(m)
	enabled := 0
	if m.Enabled {
		enabled = 1
	}
	enableCont := 0
	if m.EnableContinuation {
		enableCont = 1
	}
	enableOpenCode := 0
	if m.EnableOpenCodeSession {
		enableOpenCode = 1
	}
	_, err = db.conn.Exec(`
		INSERT INTO models (id, name, model_id, provider, base_url, api_key, refresh_token, proxy_url,
		  endpoint, disable_auto_suffix, context_window, max_tokens, reasoning_effort, image_input_mode, rate_multiplier,
		  input_price, output_price, cache_input_price, cache_output_price,
		  enable_continuation, enable_opencode_session, codex_email, codex_plan_type, codex_account_id, access_token_expires_at, codex_usage_json,
		  service_tier, enabled, sort_order, note, created_at, updated_at)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		          ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.ID, m.Name, m.ModelID, m.Provider, m.BaseURL, m.APIKey, m.RefreshToken, m.ProxyURL,
		m.Endpoint, boolToInt(m.DisableAutoSuffix), m.ContextWindow, m.MaxTokens, m.ReasoningEffort, m.ImageInputMode,
		m.RateMultiplier, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice,
		enableCont, enableOpenCode, m.CodexEmail, m.CodexPlanType, m.CodexAccountID, m.AccessTokenExpiresAt, m.CodexUsageJSON,
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
	enableOpenCode := 0
	if m.EnableOpenCodeSession {
		enableOpenCode = 1
	}
	if strings.TrimSpace(m.Endpoint) == "" {
		return fmt.Errorf("endpoint is required")
	}
	m.Endpoint = strings.TrimSpace(m.Endpoint)
	m.ProxyURL = strings.TrimSpace(m.ProxyURL)
	m.ServiceTier = normalizeServiceTier(m.ServiceTier)
	imageInputMode, err := NormalizeImageInputMode(m.ImageInputMode)
	if err != nil {
		return err
	}
	m.ImageInputMode = imageInputMode
	normalizeModelPrices(m)
	res, err := db.conn.Exec(`
		UPDATE models SET name=?, model_id=?, provider=?, base_url=?, api_key=?, refresh_token=?, proxy_url=?,
		  endpoint=?, disable_auto_suffix=?, context_window=?, max_tokens=?, reasoning_effort=?, image_input_mode=?,
		  rate_multiplier=?, input_price=?, output_price=?, cache_input_price=?, cache_output_price=?,
		  enable_continuation=?, enable_opencode_session=?, codex_email=?, codex_plan_type=?, codex_account_id=?, access_token_expires_at=?, codex_usage_json=?,
		  service_tier=?, enabled=?, sort_order=?, note=?, updated_at=?
		WHERE id=?
	`, m.Name, m.ModelID, m.Provider, m.BaseURL, m.APIKey, m.RefreshToken, m.ProxyURL,
		m.Endpoint, boolToInt(m.DisableAutoSuffix), m.ContextWindow, m.MaxTokens, m.ReasoningEffort, m.ImageInputMode,
		m.RateMultiplier, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice,
		enableCont, enableOpenCode, m.CodexEmail, m.CodexPlanType, m.CodexAccountID, m.AccessTokenExpiresAt, m.CodexUsageJSON,
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

// UpdateModelPreserveCodexCredential 原子更新模型配置，但保留数据库中的
// Codex credential fields。用于普通配置 PUT 与自动 refresh 并发的场景。
func (db *DB) UpdateModelPreserveCodexCredential(m *Model) error {
	if m == nil || strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("model is required")
	}
	if strings.TrimSpace(m.Provider) != "codex" {
		return fmt.Errorf("codex model is required")
	}
	m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if strings.TrimSpace(m.Endpoint) == "" {
		return fmt.Errorf("endpoint is required")
	}
	m.Endpoint = strings.TrimSpace(m.Endpoint)
	m.ProxyURL = strings.TrimSpace(m.ProxyURL)
	m.ServiceTier = normalizeServiceTier(m.ServiceTier)
	imageInputMode, err := NormalizeImageInputMode(m.ImageInputMode)
	if err != nil {
		return err
	}
	m.ImageInputMode = imageInputMode
	normalizeModelPrices(m)
	result, err := db.conn.Exec(`
		UPDATE models SET name=?, model_id=?, provider=?, base_url=?, proxy_url=?,
		  endpoint=?, disable_auto_suffix=?, context_window=?, max_tokens=?, reasoning_effort=?, image_input_mode=?,
		  rate_multiplier=?, input_price=?, output_price=?, cache_input_price=?, cache_output_price=?,
		  enable_continuation=?, enable_opencode_session=?, service_tier=?, enabled=?, sort_order=?, note=?, updated_at=?
		WHERE id=? AND provider='codex'
	`, m.Name, m.ModelID, m.Provider, m.BaseURL, m.ProxyURL,
		m.Endpoint, boolToInt(m.DisableAutoSuffix), m.ContextWindow, m.MaxTokens, m.ReasoningEffort, m.ImageInputMode,
		m.RateMultiplier, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice,
		boolToInt(m.EnableContinuation), boolToInt(m.EnableOpenCodeSession), m.ServiceTier, boolToInt(m.Enabled), m.SortOrder, m.Note, m.UpdatedAt, m.ID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		current, getErr := db.GetModel(m.ID)
		if getErr != nil {
			if getErr == sql.ErrNoRows {
				return fmt.Errorf("model not found: %s", m.ID)
			}
			return getErr
		}
		if current.Provider != "codex" {
			return fmt.Errorf("model provider changed: %s", m.ID)
		}
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

const (
	ImageInputAuto        = "auto"
	ImageInputSupported   = "supported"
	ImageInputUnsupported = "unsupported"
)

func NormalizeImageInputMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ImageInputAuto, nil
	}
	switch value {
	case ImageInputAuto, ImageInputSupported, ImageInputUnsupported:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported image input mode %q", value)
	}
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
		provider := strings.ToLower(strings.TrimSpace(m.Provider))
		if provider == "" || provider == "openai" || provider == "codex" {
			m.CacheOutputPrice = m.InputPrice * 1.25
		} else {
			m.CacheOutputPrice = defaults.cacheOutput
		}
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
	case "typesafe":
		// Typesafe 仅对输入 Token 收费，零输出/缓存价格表示免费。
		return defaultModelPrices{input: 0.042}
	default:
		return defaultModelPrices{input: 5, output: 30, cacheInput: 0.5, cacheOutput: 6.25}
	}
}

type CreditBreakdown struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

func (b CreditBreakdown) Total() float64 {
	return b.Input + b.Output + b.CacheRead + b.CacheWrite
}

// CalculateCreditBreakdown 按现有计费规则拆分单次请求美元成本。
func CalculateCreditBreakdown(inputTokens, outputTokens, cacheRead, cacheWrite int, m *Model) CreditBreakdown {
	if m == nil {
		m = &Model{}
	}
	cp := *m
	normalizeModelPrices(&cp)
	breakdown := billing.CalculateBreakdown(inputTokens, outputTokens, cacheRead, cacheWrite, billing.Prices{
		Input:          cp.InputPrice,
		Output:         cp.OutputPrice,
		CacheRead:      cp.CacheInputPrice,
		CacheWrite:     cp.CacheOutputPrice,
		RateMultiplier: cp.RateMultiplier,
	})
	return CreditBreakdown{
		Input:      breakdown.Input,
		Output:     breakdown.Output,
		CacheRead:  breakdown.CacheRead,
		CacheWrite: breakdown.CacheWrite,
	}
}

// CalculateCredits 返回按本地 CreditUSD 估算的积分用量。
func CalculateCredits(inputTokens, outputTokens, cacheRead, cacheWrite int, m *Model) float64 {
	if m == nil {
		m = &Model{}
	}
	cp := *m
	normalizeModelPrices(&cp)
	return billing.CalculateCredits(inputTokens, outputTokens, cacheRead, cacheWrite, billing.Prices{
		Input:          cp.InputPrice,
		Output:         cp.OutputPrice,
		CacheRead:      cp.CacheInputPrice,
		CacheWrite:     cp.CacheOutputPrice,
		RateMultiplier: cp.RateMultiplier,
	})
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

// UpdateCodexTokenCAS 仅在存储中的凭据原值仍匹配时更新凭据字段，刻意不
// 修改用量和模型配置字段。
func (db *DB) UpdateCodexTokenCAS(original, updated *Model) (*Model, error) {
	if db == nil || original == nil || updated == nil || strings.TrimSpace(original.ID) == "" {
		return nil, fmt.Errorf("model is required")
	}
	if strings.TrimSpace(original.Provider) != "codex" || strings.TrimSpace(updated.Provider) != "codex" {
		return nil, fmt.Errorf("codex model is required")
	}
	originalAPIKey := original.APIKey
	if original.CodexStoredValues || original.CodexStoredAPIKey != "" {
		originalAPIKey = original.CodexStoredAPIKey
	}
	originalRefreshToken := original.RefreshToken
	if original.CodexStoredValues || original.CodexStoredRefreshToken != "" {
		originalRefreshToken = original.CodexStoredRefreshToken
	}
	where := "id=? AND provider=? AND api_key=? AND refresh_token=? AND codex_account_id=?"
	args := []interface{}{updated.APIKey, updated.RefreshToken, updated.CodexEmail, updated.CodexPlanType, updated.CodexAccountID, updated.AccessTokenExpiresAt, original.ID, original.Provider, originalAPIKey, originalRefreshToken}
	if original.CodexStoredValues {
		args = append(args, original.CodexStoredAccountID)
	} else {
		args = append(args, original.CodexAccountID)
	}
	result, err := db.conn.Exec(`
		UPDATE models SET api_key=?, refresh_token=?, codex_email=?, codex_plan_type=?, codex_account_id=?,
		  access_token_expires_at=?
		WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows > 0 {
		return db.GetModel(original.ID)
	}
	current, err := db.GetModel(original.ID)
	if err != nil {
		return nil, err
	}
	return current, fmt.Errorf("%w: model %s", ErrCodexCredentialConflict, original.ID)
}

// CodexCredentialUpdate 描述同一 refresh generation 的一条凭据 CAS。
// Original 中的隐藏字段保留 DB auth payload 原值，Updated 只提供 token 字段。
type CodexCredentialUpdate struct {
	Original Model
	Updated  Model
}

// UpdateCodexTokenBatchCAS 在一个事务中提交同一 generation 的全部模型。
// 任一行的原始凭据不匹配时整体回滚，避免账户内出现半传播状态。
func (db *DB) UpdateCodexTokenBatchCAS(updates []CodexCredentialUpdate) (map[string]Model, error) {
	if db == nil || len(updates) == 0 {
		return nil, fmt.Errorf("credential updates are required")
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	seen := make(map[string]struct{}, len(updates))
	for _, update := range updates {
		original := update.Original
		updated := update.Updated
		if strings.TrimSpace(original.ID) == "" || strings.TrimSpace(original.Provider) != "codex" || strings.TrimSpace(updated.Provider) != "codex" {
			return nil, fmt.Errorf("codex model is required")
		}
		if _, ok := seen[original.ID]; ok {
			return nil, fmt.Errorf("duplicate credential update: %s", original.ID)
		}
		seen[original.ID] = struct{}{}
		originalAPIKey := original.APIKey
		if original.CodexStoredValues || original.CodexStoredAPIKey != "" {
			originalAPIKey = original.CodexStoredAPIKey
		}
		originalRefreshToken := original.RefreshToken
		if original.CodexStoredValues || original.CodexStoredRefreshToken != "" {
			originalRefreshToken = original.CodexStoredRefreshToken
		}
		where := "id=? AND provider=? AND api_key=? AND refresh_token=? AND codex_account_id=?"
		args := []interface{}{updated.APIKey, updated.RefreshToken, updated.CodexEmail, updated.CodexPlanType, updated.CodexAccountID, updated.AccessTokenExpiresAt, original.ID, original.Provider, originalAPIKey, originalRefreshToken}
		if original.CodexStoredValues {
			args = append(args, original.CodexStoredAccountID)
		} else {
			args = append(args, original.CodexAccountID)
		}
		result, err := tx.Exec(`
			UPDATE models SET api_key=?, refresh_token=?, codex_email=?, codex_plan_type=?, codex_account_id=?,
			  access_token_expires_at=?
			WHERE `+where, args...)
		if err != nil {
			return nil, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows == 0 {
			return nil, fmt.Errorf("%w: model %s", ErrCodexCredentialConflict, original.ID)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	result := make(map[string]Model, len(updates))
	for _, update := range updates {
		model, err := db.GetModel(update.Original.ID)
		if err != nil {
			return nil, err
		}
		result[update.Original.ID] = *model
	}
	return result, nil
}

// UpdateCodexUsage 在凭据操作成功后按 expected 凭据快照更新用量和从
// 用量响应解析出的账号信息。条件写入可避免旧用量污染已手动替换的账号。
func (db *DB) UpdateCodexUsage(expected, usage *Model) error {
	if db == nil || expected == nil || usage == nil || strings.TrimSpace(expected.ID) == "" {
		return fmt.Errorf("model is required")
	}
	if strings.TrimSpace(expected.Provider) != "codex" || strings.TrimSpace(usage.Provider) != "codex" {
		return fmt.Errorf("codex model is required")
	}
	originalAPIKey := expected.APIKey
	if expected.CodexStoredValues || expected.CodexStoredAPIKey != "" {
		originalAPIKey = expected.CodexStoredAPIKey
	}
	originalRefreshToken := expected.RefreshToken
	if expected.CodexStoredValues || expected.CodexStoredRefreshToken != "" {
		originalRefreshToken = expected.CodexStoredRefreshToken
	}
	originalAccountID := expected.CodexAccountID
	if expected.CodexStoredValues {
		originalAccountID = expected.CodexStoredAccountID
	}
	result, err := db.conn.Exec(`
		UPDATE models SET codex_usage_json=?, codex_plan_type=?, codex_email=?
		WHERE id=? AND provider='codex' AND api_key=? AND refresh_token=? AND codex_account_id=?
	`, usage.CodexUsageJSON, usage.CodexPlanType, usage.CodexEmail,
		expected.ID, originalAPIKey, originalRefreshToken, originalAccountID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("%w: model %s", ErrCodexCredentialConflict, expected.ID)
	}
	return nil
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
