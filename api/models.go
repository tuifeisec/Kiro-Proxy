// api/models.go
// 模型配置增删改查接口

package api

import (
	"context"
	"encoding/base64"
	json "github.com/goccy/go-json"
	"kiro-proxy/codex"
	"kiro-proxy/store"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		models, err := s.db.GetModels()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 脱敏敏感凭据
		for i := range models {
			models[i].APIKey = maskKey(models[i].APIKey)
			models[i].RefreshToken = maskKey(models[i].RefreshToken)
		}
		writeOK(w, models)

	case http.MethodPost:
		var m store.Model
		if err := readJSON(r, &m); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if m.Provider == "" {
			m.Provider = "openai"
		}
		if m.Provider == "codex" {
			codex.NormalizeModel(&m)
			if strings.TrimSpace(m.Name) == "" {
				m.Name = m.ModelID
			}
			if m.ModelID == "" || m.RefreshToken == "" || m.Endpoint == "" {
				writeError(w, http.StatusBadRequest, "codex 模式下 model_id, refresh_token, endpoint 不能为空")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
			defer cancel()
			if err := refreshAndFetchCodexUsage(ctx, &m); err != nil {
				writeError(w, http.StatusBadRequest, "Codex 账号预检测失败: "+err.Error())
				return
			}
		} else if m.ModelID == "" || m.BaseURL == "" || m.APIKey == "" || m.Endpoint == "" {
			writeError(w, http.StatusBadRequest, "model_id, base_url, api_key, endpoint 不能为空")
			return
		}
		// 校验 name 不可重复
		if m.Name != "" {
			if dup, _ := s.db.FindModelByName(m.Name); dup != nil {
				writeError(w, http.StatusConflict, "模型名称 \""+m.Name+"\" 已存在，请使用不同的名称")
				return
			}
		}
		m.Enabled = true
		if err := s.db.CreateModel(&m); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.router.Reload()
		m.APIKey = maskKey(m.APIKey)
		m.RefreshToken = maskKey(m.RefreshToken)
		writeJSON(w, http.StatusCreated, m)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "/api/models/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing model id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		m, err := s.db.GetModel(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "model not found")
			return
		}
		m.APIKey = maskKey(m.APIKey)
		m.RefreshToken = maskKey(m.RefreshToken)
		writeOK(w, m)

	case http.MethodPut:
		var m store.Model
		if err := readJSON(r, &m); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		m.ID = id
		// 校验 name 不可重复（排除自身）
		if m.Name != "" {
			if dup, _ := s.db.FindModelByName(m.Name); dup != nil && dup.ID != id {
				writeError(w, http.StatusConflict, "模型名称 \""+m.Name+"\" 已存在，请使用不同的名称")
				return
			}
		}
		existing, _ := s.db.GetModel(id)
		// 如果敏感凭据是脱敏值，保留原来的
		if isMasked(m.APIKey) && existing != nil {
			m.APIKey = existing.APIKey
		}
		if isMasked(m.RefreshToken) && existing != nil {
			m.RefreshToken = existing.RefreshToken
		}
		if m.Provider == "codex" {
			codex.NormalizeModel(&m)
			if strings.TrimSpace(m.Name) == "" {
				m.Name = m.ModelID
			}
			if m.ModelID == "" || m.RefreshToken == "" || m.Endpoint == "" {
				writeError(w, http.StatusBadRequest, "codex 模式下 model_id, refresh_token, endpoint 不能为空")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
			defer cancel()
			if err := refreshAndFetchCodexUsage(ctx, &m); err != nil {
				writeError(w, http.StatusBadRequest, "Codex 账号预检测失败: "+err.Error())
				return
			}
		}
		if err := s.db.UpdateModel(&m); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.router.Reload()
		m.APIKey = maskKey(m.APIKey)
		m.RefreshToken = maskKey(m.RefreshToken)
		writeOK(w, m)

	case http.MethodDelete:
		if err := s.db.DeleteModel(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.router.Reload()
		writeOK(w, map[string]bool{"ok": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

func isMasked(key string) bool {
	return strings.Contains(key, "****")
}

var codexOAuthSessions sync.Map

func (s *Server) handleStartCodexOAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, authURL, err := codex.NewOAuthSession()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建 Codex OAuth 会话失败: "+err.Error())
		return
	}
	codexOAuthSessions.Store(session.State, session)
	writeOK(w, codex.OAuthStart{State: session.State, AuthURL: authURL})
}

func (s *Server) handleExchangeCodexOAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		CallbackURL string `json:"callback_url"`
		ProxyURL    string `json:"proxy_url"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	callbackURL := strings.TrimSpace(body.CallbackURL)
	if callbackURL == "" {
		writeError(w, http.StatusBadRequest, "callback_url 不能为空")
		return
	}
	u, err := url.Parse(callbackURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		writeError(w, http.StatusBadRequest, "无效的回调 URL")
		return
	}
	q := u.Query()
	if oauthErr := q.Get("error"); oauthErr != "" {
		desc := q.Get("error_description")
		if desc == "" {
			desc = oauthErr
		}
		writeError(w, http.StatusBadRequest, "Codex OAuth 授权失败: "+desc)
		return
	}
	code := strings.TrimSpace(q.Get("code"))
	state := strings.TrimSpace(q.Get("state"))
	if code == "" || state == "" {
		writeError(w, http.StatusBadRequest, "回调 URL 必须包含 code 和 state")
		return
	}
	rawSession, ok := codexOAuthSessions.Load(state)
	if !ok {
		writeError(w, http.StatusBadRequest, "Codex OAuth 会话不存在或已过期，请重新生成授权链接")
		return
	}
	session, ok := rawSession.(*codex.OAuthSession)
	if !ok || time.Since(session.CreatedAt) > 10*time.Minute {
		codexOAuthSessions.Delete(state)
		writeError(w, http.StatusBadRequest, "Codex OAuth 会话已过期，请重新生成授权链接")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	tokens, err := codex.ExchangeCode(ctx, code, session.CodeVerifier, session.RedirectURI, body.ProxyURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Codex OAuth 换取 token 失败: "+err.Error())
		return
	}
	codexOAuthSessions.Delete(state)
	m := &store.Model{Provider: "codex"}
	codex.ApplyTokenResponse(m, tokens)
	writeOK(w, map[string]interface{}{
		"access_token":            m.APIKey,
		"refresh_token":           m.RefreshToken,
		"expires_at":              m.AccessTokenExpiresAt,
		"codex_email":             m.CodexEmail,
		"codex_plan_type":         m.CodexPlanType,
		"codex_account_id":        m.CodexAccountID,
		"access_token_expires_at": m.AccessTokenExpiresAt,
	})
}

func (s *Server) handleRefreshCodexModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		ModelID string `json:"model_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	id := strings.TrimSpace(body.ModelID)
	if id == "" {
		writeError(w, http.StatusBadRequest, "model_id 不能为空")
		return
	}
	m, err := s.db.GetModel(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "model not found")
		return
	}
	if m.Provider != "codex" {
		writeError(w, http.StatusBadRequest, "只有 Codex 模型支持刷新账号信息")
		return
	}

	log.Printf("[codex] refresh start: model_id=%s name=%s upstream_model=%s", m.ID, m.Name, m.ModelID)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := refreshAndFetchCodexUsage(ctx, m); err != nil {
		log.Printf("[codex] refresh failed: model_id=%s error=%v", m.ID, err)
		writeError(w, http.StatusBadRequest, "刷新 Codex 信息失败: "+err.Error())
		return
	}
	logCodexModelInfo(m)
	if err := s.db.UpdateCodexTokenAndUsage(m); err != nil {
		log.Printf("[codex] refresh save failed: model_id=%s error=%v", m.ID, err)
		writeError(w, http.StatusInternalServerError, "保存 Codex 信息失败: "+err.Error())
		return
	}
	s.router.Reload()
	m.APIKey = maskKey(m.APIKey)
	m.RefreshToken = maskKey(m.RefreshToken)
	writeOK(w, m)
}

func refreshAndFetchCodexUsage(ctx context.Context, m *store.Model) error {
	if err := codex.ImportAuthPayload(m); err != nil {
		return err
	}
	if strings.TrimSpace(m.APIKey) == "" {
		if err := codex.RefreshModel(ctx, m); err != nil {
			return err
		}
	}
	profile := codex.DecodeTokenProfile(m.APIKey)
	if m.CodexEmail == "" {
		m.CodexEmail = profile.Email
	}
	if m.CodexPlanType == "" {
		m.CodexPlanType = profile.PlanType
	}
	if m.CodexAccountID == "" {
		m.CodexAccountID = profile.AccountID
	}
	if m.AccessTokenExpiresAt == 0 {
		m.AccessTokenExpiresAt = profile.ExpiresAt
	}
	usage, err := codex.FetchUsage(ctx, m)
	if err != nil {
		log.Printf("[codex] fetch usage failed, continue without usage: %v", err)
		return nil
	}
	if len(usage.Raw) > 0 {
		m.CodexUsageJSON = string(usage.Raw)
	}
	return nil
}

func logCodexModelInfo(m *store.Model) {
	if m == nil {
		return
	}
	keys := []string{}
	var usage map[string]interface{}
	if strings.TrimSpace(m.CodexUsageJSON) != "" {
		if err := json.Unmarshal([]byte(m.CodexUsageJSON), &usage); err != nil {
			log.Printf("[codex] usage parse failed: model_id=%s error=%v", m.ID, err)
		} else {
			for k := range usage {
				keys = append(keys, k)
			}
			sort.Strings(keys)
		}
	}
	log.Printf("[codex] refresh result: model_id=%s email=%q plan_type=%q account_id=%q access_token_expires_at=%d usage_keys=%v usage_plan_type=%q",
		m.ID, m.CodexEmail, m.CodexPlanType, m.CodexAccountID, m.AccessTokenExpiresAt, keys, stringFromMap(usage, "plan_type"))
	logCodexTokenClaims(m.ID, m.APIKey)
	logCodexRateWindow(m.ID, "primary_window", nestedMap(usage, "rate_limit", "primary_window"))
	logCodexRateWindow(m.ID, "secondary_window", nestedMap(usage, "rate_limit", "secondary_window"))
}

func logCodexTokenClaims(modelID, token string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		log.Printf("[codex] access token is not JWT-like: model_id=%s", modelID)
		return
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		log.Printf("[codex] access token payload decode failed: model_id=%s error=%v", modelID, err)
		return
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		log.Printf("[codex] access token claims parse failed: model_id=%s error=%v", modelID, err)
		return
	}
	keys := make([]string, 0, len(claims))
	for k := range claims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	log.Printf("[codex] access token claims: model_id=%s keys=%v email=%q plan_type=%q chatgpt_plan_type=%q account_id=%q chatgpt_account_id=%q auth_claim_type=%T exp=%v",
		modelID,
		keys,
		stringFromMap(claims, "email"),
		stringFromMap(claims, "plan_type"),
		stringFromMap(claims, "chatgpt_plan_type"),
		stringFromMap(claims, "account_id"),
		stringFromMap(claims, "chatgpt_account_id"),
		claims["https://api.openai.com/auth"],
		claims["exp"],
	)
}

func logCodexRateWindow(modelID, name string, window map[string]interface{}) {
	if len(window) == 0 {
		log.Printf("[codex] usage rate_limit.%s missing: model_id=%s", name, modelID)
		return
	}
	log.Printf("[codex] usage rate_limit.%s: model_id=%s used_percent=%v reset_after_seconds=%v reset_at=%v",
		name, modelID, window["used_percent"], window["reset_after_seconds"], window["reset_at"])
}

func nestedMap(data map[string]interface{}, keys ...string) map[string]interface{} {
	var current interface{} = data
	for _, key := range keys {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current = m[key]
	}
	m, _ := current.(map[string]interface{})
	return m
}

func stringFromMap(data map[string]interface{}, key string) string {
	if data == nil {
		return ""
	}
	if s, ok := data[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
