package codex

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	json "github.com/goccy/go-json"
	"io"
	"kiro-proxy/store"
	"kiro-proxy/upstreamproxy"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	OAuthClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	OAuthTokenURL    = "https://auth.openai.com/oauth/token"
	DefaultBaseURL   = "https://chatgpt.com/backend-api"
	DefaultEndpoint  = "/codex/responses"
	RefreshMarginSec = 300
)

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

type TokenProfile struct {
	Email     string
	PlanType  string
	AccountID string
	UserID    string
	ExpiresAt int64
}

type UsageResponse struct {
	RateLimit interface{} `json:"rate_limit,omitempty"`
	Raw       []byte      `json:"-"`
}

func NormalizeModel(m *store.Model) {
	if m == nil || m.Provider != "codex" {
		return
	}
	if strings.TrimSpace(m.BaseURL) == "" {
		m.BaseURL = DefaultBaseURL
	}
	if strings.TrimSpace(m.Endpoint) == "" {
		m.Endpoint = DefaultEndpoint
	}
}

func EnsureAccessToken(ctx context.Context, m *store.Model) error {
	NormalizeModel(m)
	if m == nil || m.Provider != "codex" {
		return nil
	}
	if err := ImportAuthPayload(m); err != nil {
		return err
	}
	now := time.Now().Unix()
	if strings.TrimSpace(m.APIKey) != "" && m.AccessTokenExpiresAt-now > RefreshMarginSec {
		return nil
	}
	if strings.TrimSpace(m.RefreshToken) == "" {
		return fmt.Errorf("codex access_token 已过期且没有 refresh_token")
	}
	return RefreshModel(ctx, m)
}

func RefreshModel(ctx context.Context, m *store.Model) error {
	NormalizeModel(m)
	if err := ImportAuthPayload(m); err != nil {
		return err
	}
	tokens, err := RefreshAccessToken(ctx, m.RefreshToken, m.ProxyURL)
	if err != nil {
		return err
	}
	applyTokenResponse(m, tokens)
	return nil
}

func ImportAuthPayload(m *store.Model) error {
	if m == nil || m.Provider != "codex" {
		return nil
	}
	raw := strings.TrimSpace(m.RefreshToken)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return nil
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresAt    int64  `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return fmt.Errorf("解析 Codex auth JSON 失败: %w", err)
	}
	if strings.TrimSpace(payload.RefreshToken) == "" {
		return fmt.Errorf("Codex auth JSON 缺少 refresh_token")
	}
	m.RefreshToken = strings.TrimSpace(payload.RefreshToken)
	if strings.TrimSpace(payload.AccessToken) != "" {
		m.APIKey = strings.TrimSpace(payload.AccessToken)
		profile := DecodeTokenProfile(m.APIKey)
		if payload.ExpiresAt > 0 {
			m.AccessTokenExpiresAt = payload.ExpiresAt
		} else {
			m.AccessTokenExpiresAt = profile.ExpiresAt
		}
		m.CodexEmail = profile.Email
		m.CodexPlanType = profile.PlanType
		m.CodexAccountID = profile.AccountID
	}
	return nil
}

func applyTokenResponse(m *store.Model, tokens *TokenResponse) {
	m.APIKey = tokens.AccessToken
	if strings.TrimSpace(tokens.RefreshToken) != "" {
		m.RefreshToken = tokens.RefreshToken
	}
	profile := DecodeTokenProfile(tokens.AccessToken)
	if tokens.ExpiresIn > 0 {
		m.AccessTokenExpiresAt = time.Now().Unix() + tokens.ExpiresIn
	} else if profile.ExpiresAt > 0 {
		m.AccessTokenExpiresAt = profile.ExpiresAt
	}
	m.CodexEmail = profile.Email
	m.CodexPlanType = profile.PlanType
	m.CodexAccountID = profile.AccountID
}
func RefreshAccessToken(ctx context.Context, refreshToken, proxyAddr string) (*TokenResponse, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh_token 不能为空")
	}
	body := url.Values{}
	body.Set("grant_type", "refresh_token")
	body.Set("client_id", OAuthClientID)
	body.Set("refresh_token", refreshToken)

	client, err := upstreamproxy.NewClient(proxyAddr, 60*time.Second)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthTokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Codex Desktop/260202.0859 (darwin; arm64)")
	req.Header.Set("sec-ch-ua", `"Chromium";v="136", "Not:A-Brand";v="24"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"macOS"`)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Origin", "https://auth.openai.com")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("刷新 Codex token 失败，HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out TokenResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("解析 Codex token 响应失败: %w", err)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return nil, fmt.Errorf("Codex token 响应缺少 access_token")
	}
	return &out, nil
}

func FetchUsage(ctx context.Context, m *store.Model) (*UsageResponse, error) {
	NormalizeModel(m)
	if err := EnsureAccessToken(ctx, m); err != nil {
		return nil, err
	}
	client, err := upstreamproxy.NewClient(m.ProxyURL, 30*time.Second)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimRight(m.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	paths := []string{"/codex/usage", "/wham/usage"}
	var lastErr error
	for _, path := range paths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
		if err != nil {
			return nil, err
		}
		ApplyHeaders(req, m.APIKey, m.CodexAccountID, false)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("获取 Codex 用量失败，%s HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
			continue
		}
		var parsed UsageResponse
		_ = json.Unmarshal(body, &parsed)
		parsed.Raw = body
		applyUsageInfo(m, body)
		return &parsed, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("获取 Codex 用量失败")
}

func applyUsageInfo(m *store.Model, body []byte) {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return
	}
	if plan := stringFromAny(data["plan_type"]); plan != "" {
		m.CodexPlanType = plan
	}
	if email := stringFromAny(data["email"]); email != "" && m.CodexEmail == "" {
		m.CodexEmail = email
	}
}

func ApplyHeaders(req *http.Request, token, accountID string, withContentType bool) {
	req.Header.Set("Authorization", "Bearer "+token)
	if accountID == "" {
		accountID = DecodeTokenProfile(token).AccountID
	}
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	req.Header.Set("originator", "Codex Desktop")
	req.Header.Set("User-Agent", "Codex Desktop/260202.0859 (darwin; arm64)")
	req.Header.Set("sec-ch-ua", `"Chromium";v="136", "Not:A-Brand";v="24"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"macOS"`)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-dest", "empty")
	if withContentType {
		req.Header.Set("Content-Type", "application/json")
	}
}

func DecodeTokenProfile(token string) TokenProfile {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return TokenProfile{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return TokenProfile{}
	}
	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		return TokenProfile{}
	}
	profile := TokenProfile{
		Email:     firstNestedString(data, []string{"https://api.openai.com/profile", "email"}, []string{"email"}),
		PlanType:  firstNestedString(data, []string{"https://api.openai.com/auth", "chatgpt_plan_type"}, []string{"https://api.openai.com/profile", "chatgpt_plan_type"}, []string{"chatgpt_plan_type"}, []string{"plan_type"}),
		AccountID: firstNestedString(data, []string{"https://api.openai.com/auth", "chatgpt_account_id"}, []string{"https://api.openai.com/auth", "account_id"}, []string{"account_id"}, []string{"chatgpt_account_id"}),
		UserID:    firstNestedString(data, []string{"https://api.openai.com/auth", "chatgpt_user_id"}, []string{"https://api.openai.com/profile", "chatgpt_user_id"}, []string{"chatgpt_user_id"}, []string{"sub"}),
	}
	if exp, ok := data["exp"].(float64); ok {
		profile.ExpiresAt = int64(exp)
	}
	if profile.AccountID == "" {
		profile.AccountID = findNestedAccountID(data)
	}
	return profile
}

func firstString(data map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := data[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstNestedString(data map[string]interface{}, paths ...[]string) string {
	for _, path := range paths {
		var current interface{} = data
		for _, key := range path {
			m, ok := current.(map[string]interface{})
			if !ok {
				current = nil
				break
			}
			current = m[key]
		}
		if s := stringFromAny(current); s != "" {
			return s
		}
	}
	return ""
}

func findNestedAccountID(v interface{}) string {
	switch x := v.(type) {
	case map[string]interface{}:
		for k, val := range x {
			if strings.Contains(strings.ToLower(k), "account") {
				if s, ok := val.(string); ok && s != "" {
					return s
				}
			}
			if found := findNestedAccountID(val); found != "" {
				return found
			}
		}
	case []interface{}:
		for _, item := range x {
			if found := findNestedAccountID(item); found != "" {
				return found
			}
		}
	}
	return ""
}

func NewRequestBodyFromResponses(body []byte) []byte {
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body
	}
	parsed["store"] = false
	parsed["stream"] = true

	// Codex backend is close to Responses API, but not identical.
	// Remove/reshape fields accepted by /v1/responses but rejected by /codex/responses.
	delete(parsed, "user")
	delete(parsed, "previous_response_id") // HTTP SSE Codex endpoint does not support it safely.

	if effort, ok := parsed["reasoning_effort"]; ok {
		delete(parsed, "reasoning_effort")
		if s, ok := effort.(string); ok && strings.TrimSpace(s) != "" {
			parsed["reasoning"] = map[string]interface{}{
				"effort":  strings.TrimSpace(s),
				"summary": "auto",
			}
		}
	}
	if input, ok := parsed["input"].([]interface{}); ok {
		parsed["input"] = sanitizeCodexInput(input)
	}
	if _, ok := parsed["instructions"]; !ok {
		parsed["instructions"] = ""
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		return body
	}
	return bytes.TrimSpace(out)
}

func sanitizeCodexInput(input []interface{}) []interface{} {
	out := make([]interface{}, 0, len(input))
	for _, item := range input {
		m, ok := item.(map[string]interface{})
		if !ok {
			out = append(out, item)
			continue
		}
		if m["type"] == "message" {
			delete(m, "type")
		}
		out = append(out, m)
	}
	return out
}

func stringFromAny(v interface{}) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	default:
		return ""
	}
}
