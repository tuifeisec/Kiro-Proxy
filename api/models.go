// api/models.go
// 模型配置增删改查接口

package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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
		m.Provider = strings.ToLower(strings.TrimSpace(m.Provider))
		if m.Provider == "" {
			m.Provider = "openai"
		}
		imageInputMode, err := store.NormalizeImageInputMode(m.ImageInputMode)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		m.ImageInputMode = imageInputMode
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
			// 创建入口允许客户端提供稳定 ID，但该 ID 尚不存在于 DB；预检测
			// 必须明确走 ephemeral/import 路径，不能被 manager 的 NoRows 冲突拦截。
			requestedID := m.ID
			m.ID = ""
			precheckErr := s.refreshAndFetchCodexUsage(ctx, &m, false)
			m.ID = requestedID
			if precheckErr != nil {
				status := http.StatusBadRequest
				if errors.Is(precheckErr, store.ErrCodexCredentialConflict) {
					status = http.StatusConflict
				}
				writeError(w, status, "Codex 账号预检测失败: "+precheckErr.Error())
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
		if err := s.validateModelGroupConflict(m.Name, m.ModelID); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
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
		m.Provider = strings.ToLower(strings.TrimSpace(m.Provider))
		imageInputMode, err := store.NormalizeImageInputMode(m.ImageInputMode)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		m.ImageInputMode = imageInputMode
		// 校验 name 不可重复（排除自身）
		if m.Name != "" {
			if dup, _ := s.db.FindModelByName(m.Name); dup != nil && dup.ID != id {
				writeError(w, http.StatusConflict, "模型名称 \""+m.Name+"\" 已存在，请使用不同的名称")
				return
			}
		}
		existing, existingErr := s.db.GetModel(id)
		if existingErr != nil || existing == nil {
			writeError(w, http.StatusNotFound, "model not found")
			return
		}
		// DB 旧值按运行时的精确语义识别；非规范旧 Provider 需要显式提供凭据后修正。
		providerChanged := existing.Provider != m.Provider
		apiKeyMasked := isMasked(m.APIKey)
		refreshTokenMasked := isMasked(m.RefreshToken)
		if providerChanged {
			if m.Provider == "codex" {
				if strings.TrimSpace(m.RefreshToken) == "" || refreshTokenMasked {
					writeError(w, http.StatusBadRequest, "切换到 Codex 必须提供新的 refresh_token 或完整 auth JSON")
					return
				}
			} else if strings.TrimSpace(m.APIKey) == "" || apiKeyMasked {
				writeError(w, http.StatusBadRequest, "切换 provider 必须提供新的 api_key")
				return
			}
		}
		// 如果敏感凭据是脱敏值，保留原来的
		if !providerChanged && apiKeyMasked && existing != nil {
			m.APIKey = existing.APIKey
		}
		if !providerChanged && refreshTokenMasked && existing != nil {
			m.RefreshToken = existing.RefreshToken
		}
		if providerChanged && m.Provider != "codex" {
			// provider 切换不能携带旧 Codex 的身份和用量字段。
			m.RefreshToken = ""
			m.CodexEmail = ""
			m.CodexPlanType = ""
			m.CodexAccountID = ""
			m.AccessTokenExpiresAt = 0
			m.CodexUsageJSON = ""
		}
		manualCodexCredential := existing != nil && codexCredentialChanged(existing, &m)
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
			if err := s.refreshAndFetchCodexUsage(ctx, &m, manualCodexCredential, existing); err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, store.ErrCodexCredentialConflict) {
					status = http.StatusConflict
				}
				writeError(w, status, "Codex 账号预检测失败: "+err.Error())
				return
			}
		}
		if err := s.validateModelGroupConflict(m.Name, m.ModelID); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		var updateErr error
		if existing != nil && m.Provider == "codex" && !manualCodexCredential {
			updateErr = s.db.UpdateModelPreserveCodexCredential(&m)
		} else {
			updateErr = s.db.UpdateModel(&m)
		}
		if updateErr != nil {
			writeError(w, http.StatusInternalServerError, updateErr.Error())
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

const (
	codexOAuthSessionTTL      = 10 * time.Minute
	codexOAuthSessionMaxItems = 256
)

func storeCodexOAuthSession(session *codex.OAuthSession) {
	now := time.Now()
	type storedSession struct {
		state     string
		createdAt time.Time
	}
	active := make([]storedSession, 0, codexOAuthSessionMaxItems)
	codexOAuthSessions.Range(func(key, value interface{}) bool {
		state, stateOK := key.(string)
		stored, sessionOK := value.(*codex.OAuthSession)
		if !stateOK || !sessionOK || now.Sub(stored.CreatedAt) > codexOAuthSessionTTL {
			codexOAuthSessions.Delete(key)
			return true
		}
		active = append(active, storedSession{state: state, createdAt: stored.CreatedAt})
		return true
	})
	for len(active) >= codexOAuthSessionMaxItems {
		oldest := 0
		for i := 1; i < len(active); i++ {
			if active[i].createdAt.Before(active[oldest].createdAt) {
				oldest = i
			}
		}
		codexOAuthSessions.Delete(active[oldest].state)
		active[oldest] = active[len(active)-1]
		active = active[:len(active)-1]
	}
	codexOAuthSessions.Store(session.State, session)
}

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
	storeCodexOAuthSession(session)
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
	if !ok || time.Since(session.CreatedAt) > codexOAuthSessionTTL {
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
	refreshed, err := s.credentials.ForceRefresh(ctx, *m)
	if err != nil {
		log.Printf("[codex] refresh failed: model_id=%s error=%v", m.ID, err)
		writeError(w, http.StatusBadRequest, "刷新 Codex 信息失败: "+err.Error())
		return
	}
	expected := refreshed
	if usage, usageErr := codex.FetchUsage(ctx, &refreshed); usageErr == nil && usage != nil && len(usage.Raw) > 0 {
		refreshed.CodexUsageJSON = string(usage.Raw)
		if err := s.db.UpdateCodexUsage(&expected, &refreshed); err != nil {
			log.Printf("[codex] usage save failed: model_id=%s error=%v", refreshed.ID, err)
			status := http.StatusInternalServerError
			if errors.Is(err, store.ErrCodexCredentialConflict) {
				status = http.StatusConflict
			}
			writeError(w, status, "保存 Codex 用量失败: "+err.Error())
			return
		}
	} else if usageErr != nil {
		log.Printf("[codex] fetch usage failed, continue without usage: %v", usageErr)
	}
	logCodexModelInfo(&refreshed)
	// ForceRefresh 已通过 CAS 持久化 token 字段。用量返回给调用方并单独
	// 写入，避免并发刷新时把旧 token 一起写回。
	s.router.Reload()
	refreshed.APIKey = maskKey(refreshed.APIKey)
	refreshed.RefreshToken = maskKey(refreshed.RefreshToken)
	writeOK(w, refreshed)
}

func (s *Server) refreshAndFetchCodexUsage(ctx context.Context, m *store.Model, manual bool, existing ...*store.Model) error {
	if m == nil {
		return fmt.Errorf("model is required")
	}
	// 普通 PUT 的输入只代表待保存配置；凭据预检测必须读取 DB 中的
	// existing 快照，避免 incoming 配置携带旧 token 覆盖自动轮换结果。
	var source *store.Model
	if !manual && len(existing) > 0 && existing[0] != nil {
		source = existing[0]
	}
	var refreshed store.Model
	var err error
	if manual {
		// 手动提供的凭据属于明确导入。本次操作清除 DB identity，避免 Ensure
		// 用旧 router 快照替换手动凭据。refresh token 变化时也清除输入中
		// 可能由脱敏凭据继承的 access/profile/usage，避免拼出混合账号。
		input := *m
		input.ID = ""
		var existingModel *store.Model
		if len(existing) > 0 {
			existingModel = existing[0]
		}
		parsed := store.Model{Provider: "codex", RefreshToken: input.RefreshToken}
		if err := codex.ImportAuthPayload(&parsed); err != nil {
			return err
		}
		var previous store.Model
		if existingModel != nil {
			previous = *existingModel
			if err := codex.ImportAuthPayload(&previous); err != nil {
				return err
			}
		}
		sameRefresh := existingModel != nil && previous.Provider == "codex" && strings.TrimSpace(parsed.RefreshToken) == strings.TrimSpace(previous.RefreshToken)
		if sameRefresh && ((parsed.APIKey != "" && parsed.APIKey != previous.APIKey) || strings.TrimSpace(input.APIKey) != strings.TrimSpace(existingModel.APIKey)) {
			return fmt.Errorf("手动 Codex 凭据不完整：请提供新的 refresh_token 或完整 auth JSON")
		}
		if sameRefresh {
			// 仅包装不同仍属于已知同代；保留其身份，使手动与自动刷新共用 flight。
			// 同时保留 DB ID，ownership 内才能重读迟到请求的最新代次。
			input = previous
			source = existingModel
		} else {
			input.APIKey = ""
			input.AccessTokenExpiresAt = 0
			input.CodexAccountID = ""
			input.CodexEmail = ""
			input.CodexPlanType = ""
			input.CodexUsageJSON = ""
		}
		refreshed, err = s.credentials.ForceRefresh(ctx, input)
		refreshed.ID = m.ID
	} else if source != nil {
		refreshed, err = s.credentials.Ensure(ctx, *source)
	} else {
		refreshed, err = s.credentials.Ensure(ctx, *m)
	}
	if err != nil {
		return err
	}
	expected := refreshed
	usage, err := codex.FetchUsage(ctx, &refreshed)
	if err != nil {
		log.Printf("[codex] fetch usage failed, continue without usage: %v", err)
		if source != nil {
			copyCodexCredentialsAndUsage(m, &refreshed)
		} else {
			*m = refreshed
		}
		return nil
	}
	if len(usage.Raw) > 0 {
		refreshed.CodexUsageJSON = string(usage.Raw)
		if source != nil {
			if err := s.db.UpdateCodexUsage(&expected, &refreshed); err != nil {
				return err
			}
		}
	}
	if source != nil {
		copyCodexCredentialsAndUsage(m, &refreshed)
		return nil
	}
	*m = refreshed
	return nil
}

func copyCodexCredentialsAndUsage(dst, src *store.Model) {
	if dst == nil || src == nil {
		return
	}
	dst.APIKey = src.APIKey
	dst.RefreshToken = src.RefreshToken
	dst.CodexEmail = src.CodexEmail
	dst.CodexPlanType = src.CodexPlanType
	dst.CodexAccountID = src.CodexAccountID
	dst.AccessTokenExpiresAt = src.AccessTokenExpiresAt
	dst.CodexUsageJSON = src.CodexUsageJSON
}

// 为兼容共享 manager 引入前的包内调用方保留；生产 handler 使用上面注入的
// server manager。
func refreshAndFetchCodexUsage(ctx context.Context, m *store.Model) error {
	manager := codex.NewCredentialManager(nil)
	refreshed, err := manager.Ensure(ctx, *m)
	if err != nil {
		return err
	}
	usage, err := codex.FetchUsage(ctx, &refreshed)
	if err != nil {
		return nil
	}
	if usage != nil && len(usage.Raw) > 0 {
		refreshed.CodexUsageJSON = string(usage.Raw)
	}
	*m = refreshed
	return nil
}

func codexCredentialChanged(existing, incoming *store.Model) bool {
	if existing == nil || incoming == nil {
		return false
	}
	return existing.APIKey != incoming.APIKey || existing.RefreshToken != incoming.RefreshToken ||
		existing.Provider != incoming.Provider || existing.CodexAccountID != incoming.CodexAccountID
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
