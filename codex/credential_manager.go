package codex

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"kiro-proxy/store"
	"strings"
	"sync"
	"time"
)

// TokenRefresher 是 CredentialManager 使用的最小刷新依赖。
// 默认实现为 RefreshAccessToken，测试和本地导入路径可注入确定性实现。
type TokenRefresher func(context.Context, string, string) (*TokenResponse, error)

// CredentialManager 协调代理、API 和模型测试入口共享的 Codex 凭据。
// 这里刻意接收并返回 Model 值，刷新凭据时不会修改路由快照。
type CredentialManager struct {
	db        *store.DB
	refresher TokenRefresher

	mu       sync.Mutex
	inFlight map[string]*credentialRefresh
	pending  map[string]pendingCredential
}

// credentialRefresh 的 done 覆盖 provider 刷新和同代凭据集合 CAS，
// 因此 flight 释放前不会让另一个请求重新消费旧 refresh_token。
type credentialRefresh struct {
	done       chan struct{}
	err        error
	snapshot   store.Model
	hasResult  bool
	persistErr error
	retry      bool
}

// pendingCredential 保存一整代 token 的 absolute snapshot。DB 暂时不可写时，
// 后续 Ensure 会重试落库，而不会再次调用 provider 消费旧 refresh_token。
type pendingCredential struct {
	key      string
	original store.Model
	updated  store.Model
}

// NewCredentialManager 创建进程级凭据协调器。
func NewCredentialManager(db *store.DB) *CredentialManager {
	return NewCredentialManagerWithRefresher(db, RefreshAccessToken)
}

// NewCredentialManagerWithRefresher 创建可注入 token endpoint 的 manager，
// 也用于可重复执行的本地测试。
func NewCredentialManagerWithRefresher(db *store.DB, refresher TokenRefresher) *CredentialManager {
	if refresher == nil {
		refresher = RefreshAccessToken
	}
	return &CredentialManager{
		db:        db,
		refresher: refresher,
		inFlight:  make(map[string]*credentialRefresh),
		pending:   make(map[string]pendingCredential),
	}
}

// Ensure 返回当前独立凭据快照；判断是否需要刷新前会读取最新持久化模型。
func (m *CredentialManager) Ensure(ctx context.Context, model store.Model) (store.Model, error) {
	return m.ensure(ctx, model, false)
}

// ForceRefresh 即使当前 access token 仍有效也会刷新凭据，返回值独立于输入模型。
func (m *CredentialManager) ForceRefresh(ctx context.Context, model store.Model) (store.Model, error) {
	return m.ensure(ctx, model, true)
}

// EnsureModel 为已持有模型指针的调用方提供适配，仍不会修改指针指向的模型。
func (m *CredentialManager) EnsureModel(ctx context.Context, model *store.Model) (store.Model, error) {
	if model == nil {
		return store.Model{}, fmt.Errorf("model is required")
	}
	return m.Ensure(ctx, *model)
}

// ForceRefreshModel 为 ForceRefresh 提供指针适配。
func (m *CredentialManager) ForceRefreshModel(ctx context.Context, model *store.Model) (store.Model, error) {
	if model == nil {
		return store.Model{}, fmt.Errorf("model is required")
	}
	return m.ForceRefresh(ctx, *model)
}

func (m *CredentialManager) ensure(ctx context.Context, input store.Model, force bool) (store.Model, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return store.Model{}, err
		}
		result, retry, err := m.ensureAttempt(ctx, input, force)
		if !retry {
			return result, err
		}
	}
}

func (m *CredentialManager) ensureAttempt(ctx context.Context, input store.Model, force bool) (store.Model, bool, error) {
	current, err := m.latestModel(input)
	if err != nil {
		return store.Model{}, false, err
	}
	if strings.TrimSpace(current.Provider) != "codex" {
		return current, false, nil
	}
	key := credentialKey(current)
	_, hasPending := m.pendingFor(current, key)
	if !force && !hasPending && tokenIsValid(&current) {
		return current, false, nil
	}
	if strings.TrimSpace(current.RefreshToken) == "" {
		return store.Model{}, false, fmt.Errorf("codex access_token 已过期且没有 refresh_token")
	}

	refreshKey := credentialKey(current)
	call, leader := m.beginRefresh(refreshKey)
	if leader {
		m.runRefresh(ctx, input, refreshKey, force, call)
		// 必须在 provider 刷新、集合 CAS 和 pending 发布之后释放 flight。
		m.endRefresh(refreshKey, call)
	} else {
		select {
		case <-call.done:
		case <-ctx.Done():
			return store.Model{}, false, ctx.Err()
		}
	}
	if call.retry {
		return store.Model{}, true, nil
	}
	if call.err != nil {
		// leader 取消时不能让仍存活的 follower 永久丢失刷新机会。
		// 失败 flight 结束后，follower 使用自己的 context 重试。
		if !leader && ctx.Err() == nil && (errors.Is(call.err, context.Canceled) || errors.Is(call.err, context.DeadlineExceeded)) {
			return store.Model{}, true, nil
		}
		return store.Model{}, false, call.err
	}
	if !call.hasResult {
		return store.Model{}, false, fmt.Errorf("Codex 刷新没有结果")
	}
	if call.persistErr != nil {
		return store.Model{}, false, call.persistErr
	}
	// DB 是唯一成功发布点。leader/follower 都重新验证路由，禁止将并发编辑后
	// 的新账户凭据配到旧目标上；follower 不另开单行补写绕过事务失败。
	latest, latestErr := m.latestModel(input)
	if latestErr != nil {
		return store.Model{}, false, latestErr
	}
	if !latest.CodexStoredValues {
		copyCodexTokenFields(&latest, &call.snapshot)
		return latest, false, nil
	}
	if latest.APIKey != call.snapshot.APIKey && !tokenIsValid(&latest) {
		return store.Model{}, false, fmt.Errorf("%w: credentials changed after refresh", store.ErrCodexCredentialConflict)
	}
	return latest, false, nil
}

func (m *CredentialManager) runRefresh(ctx context.Context, input store.Model, refreshKey string, force bool, call *credentialRefresh) {
	// 获取刷新所有权后再次读取 DB，避免 ownership 建立前的旧快照继续刷新。
	// latestModel 同时校验路由快照；凭据代次变化则旧 flight 不能越权刷新。
	ownedCurrent, latestErr := m.latestModel(input)
	if latestErr != nil {
		call.err = latestErr
	} else if credentialKey(ownedCurrent) != refreshKey {
		// 正常轮换已经传播到本行：重新选取最新 generation，不能使用旧 token。
		call.retry = true
	} else if pending, ok := m.pendingFor(ownedCurrent, refreshKey); ok {
		call.snapshot, call.persistErr = m.persistAccount(ctx, ownedCurrent, pending.updated, refreshKey)
		call.hasResult = true
	} else if !force && tokenIsValid(&ownedCurrent) {
		call.snapshot = ownedCurrent
		call.hasResult = true
	} else {
		tokens, refreshErr := m.refreshTokens(ctx, ownedCurrent)
		if refreshErr != nil {
			call.err = refreshErr
		} else if tokens == nil || strings.TrimSpace(tokens.AccessToken) == "" {
			call.err = fmt.Errorf("Codex 刷新未返回 access_token")
		} else {
			updated := ownedCurrent
			// ExpiresIn 只在 provider 返回处换算一次；后续 peer、
			// follower 和 pending 都复制这个 absolute 时间戳。
			applyTokenResponse(&updated, tokens)
			call.snapshot, call.persistErr = m.persistAccount(ctx, ownedCurrent, updated, refreshKey)
			call.hasResult = true
		}
	}
}

func (m *CredentialManager) latestModel(input store.Model) (store.Model, error) {
	requested := input
	requested.CodexStoredValues = false
	NormalizeModel(&requested)
	if err := ImportAuthPayload(&requested); err != nil {
		return store.Model{}, err
	}
	if m == nil || m.db == nil || strings.TrimSpace(requested.ID) == "" {
		// 没有 DB 记录的模型属于明确的 ephemeral/import 路径。
		return requested, nil
	}
	latest, err := m.db.GetModel(requested.ID)
	if err == nil {
		storedAPIKey := latest.APIKey
		storedRefreshToken := latest.RefreshToken
		current := *latest
		current.CodexStoredAPIKey = storedAPIKey
		current.CodexStoredRefreshToken = storedRefreshToken
		current.CodexStoredAccountID = latest.CodexAccountID
		current.CodexStoredValues = true
		NormalizeModel(&current)
		if err := ImportAuthPayload(&current); err != nil {
			return store.Model{}, err
		}
		if strings.EqualFold(strings.TrimSpace(requested.Provider), "codex") && (!strings.EqualFold(strings.TrimSpace(current.Provider), "codex") || !routeCompatible(requested, current)) {
			return store.Model{}, fmt.Errorf("%w: route changed while reading credentials", store.ErrCodexCredentialConflict)
		}
		return current, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.Model{}, fmt.Errorf("%w: model %s no longer exists", store.ErrCodexCredentialConflict, requested.ID)
	}
	return store.Model{}, err
}

func routeCompatible(requested, current store.Model) bool {
	if !strings.EqualFold(strings.TrimSpace(requested.Provider), strings.TrimSpace(current.Provider)) {
		return false
	}
	if effectiveBaseURL(requested) != effectiveBaseURL(current) {
		return false
	}
	if strings.TrimSpace(requested.Endpoint) != strings.TrimSpace(current.Endpoint) || strings.TrimSpace(requested.ProxyURL) != strings.TrimSpace(current.ProxyURL) || requested.DisableAutoSuffix != current.DisableAutoSuffix {
		return false
	}
	requestedAccount := credentialIdentity(requested)
	currentAccount := credentialIdentity(current)
	return requestedAccount == "" || currentAccount == "" || requestedAccount == currentAccount
}

func effectiveBaseURL(model store.Model) string {
	NormalizeModel(&model)
	return strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")
}

func credentialIdentity(model store.Model) string {
	if account := strings.TrimSpace(model.CodexAccountID); account != "" {
		return "account:" + account
	}
	profile := DecodeTokenProfile(model.APIKey)
	if account := strings.TrimSpace(profile.AccountID); account != "" {
		return "account:" + account
	}
	if userID := strings.TrimSpace(profile.UserID); userID != "" {
		return "profile:" + userID
	}
	if email := strings.TrimSpace(model.CodexEmail); email != "" {
		return "email:" + strings.ToLower(email)
	}
	if email := strings.TrimSpace(profile.Email); email != "" {
		return "email:" + strings.ToLower(email)
	}
	return ""
}

func (m *CredentialManager) refreshTokens(ctx context.Context, current store.Model) (*TokenResponse, error) {
	if m == nil || m.refresher == nil {
		return nil, fmt.Errorf("Codex credential refresher 未配置")
	}
	return m.refresher(ctx, strings.TrimSpace(current.RefreshToken), strings.TrimSpace(current.ProxyURL))
}

func (m *CredentialManager) beginRefresh(key string) (*credentialRefresh, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if call, ok := m.inFlight[key]; ok {
		return call, false
	}
	call := &credentialRefresh{done: make(chan struct{})}
	m.inFlight[key] = call
	return call, true
}

func (m *CredentialManager) endRefresh(key string, call *credentialRefresh) {
	m.mu.Lock()
	if m.inFlight[key] == call {
		delete(m.inFlight, key)
	}
	close(call.done)
	m.mu.Unlock()
}

func (m *CredentialManager) pendingFor(model store.Model, key string) (pendingCredential, bool) {
	if m == nil || m.db == nil || strings.TrimSpace(model.ID) == "" {
		return pendingCredential{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if pending, ok := m.pending[pendingKey(model.ID, key)]; ok {
		return pending, true
	}
	// provider 刷新按账户和 refresh generation 共享，而 DB 持久化仍按模型进行。
	for _, pending := range m.pending {
		if pending.key != key || !sameCredentialGeneration(model, pending.original) {
			continue
		}
		candidate := model
		copyCodexTokenFields(&candidate, &pending.updated)
		return pendingCredential{key: key, original: model, updated: candidate}, true
	}
	return pendingCredential{}, false
}

func (m *CredentialManager) persistAccount(ctx context.Context, original, updated store.Model, key string) (store.Model, error) {
	_ = ctx
	if m == nil || m.db == nil || !original.CodexStoredValues {
		return updated, nil
	}
	// 在任何本地读写失败之前保留整代结果。成功提交或确认该代已无引用后清除。
	m.savePending(pendingCredential{key: key, original: original, updated: updated})
	models, err := m.db.GetModels()
	if err != nil {
		return updated, err
	}
	updates := make([]store.CodexCredentialUpdate, 0, len(models))
	for _, model := range models {
		if strings.TrimSpace(model.Provider) != "codex" {
			continue
		}
		stored := model
		stored.CodexStoredAPIKey = model.APIKey
		stored.CodexStoredRefreshToken = model.RefreshToken
		stored.CodexStoredAccountID = model.CodexAccountID
		stored.CodexStoredValues = true
		NormalizeModel(&stored)
		if err := ImportAuthPayload(&stored); err != nil {
			// 无法解码的其他模型不是可匹配的 generation；其自身请求仍返回导入错误。
			continue
		}
		if credentialKey(stored) != key || !sameCredentialGeneration(stored, original) {
			continue
		}
		candidate := stored
		copyCodexTokenFields(&candidate, &updated)
		updates = append(updates, store.CodexCredentialUpdate{Original: stored, Updated: candidate})
	}
	if len(updates) == 0 {
		// 所有旧代引用均已被手动替换或删除，没有可传播的记录。
		m.clearPending(original.ID, key)
		return m.latestModel(original)
	}
	saved, err := m.db.UpdateCodexTokenBatchCAS(updates)
	if err != nil {
		return updated, err
	}
	m.clearPending(original.ID, key)
	if result, ok := saved[original.ID]; ok {
		return result, nil
	}
	return m.latestModel(original)
}

func (m *CredentialManager) savePending(pending pendingCredential) {
	if m == nil || strings.TrimSpace(pending.original.ID) == "" {
		return
	}
	m.mu.Lock()
	m.pending[pendingKey(pending.original.ID, pending.key)] = pending
	m.mu.Unlock()
}

func copyCodexTokenFields(dst, src *store.Model) {
	if dst == nil || src == nil {
		return
	}
	dst.APIKey = src.APIKey
	dst.RefreshToken = src.RefreshToken
	dst.CodexEmail = src.CodexEmail
	dst.CodexPlanType = src.CodexPlanType
	dst.CodexAccountID = src.CodexAccountID
	dst.AccessTokenExpiresAt = src.AccessTokenExpiresAt
}

func sameCredentialGeneration(current, other store.Model) bool {
	currentRefresh := strings.TrimSpace(current.RefreshToken)
	otherRefresh := strings.TrimSpace(other.RefreshToken)
	if currentRefresh != "" && otherRefresh != "" {
		return currentRefresh == otherRefresh
	}
	return strings.TrimSpace(current.APIKey) != "" && current.APIKey == other.APIKey
}

func (m *CredentialManager) clearPending(modelID, key string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	for entryKey, pending := range m.pending {
		if pending.key == key {
			delete(m.pending, entryKey)
		}
	}
	m.mu.Unlock()
}

func pendingKey(modelID, key string) string { return strings.TrimSpace(modelID) + "|" + key }

func tokenIsValid(model *store.Model) bool {
	if model == nil || strings.TrimSpace(model.APIKey) == "" {
		return false
	}
	return model.AccessTokenExpiresAt-time.Now().Unix() > RefreshMarginSec
}

func credentialKey(model store.Model) string {
	identity := credentialIdentity(model)
	refresh := strings.TrimSpace(model.RefreshToken)
	hash := sha256.Sum256([]byte(refresh))
	generation := fmt.Sprintf("refresh:%x", hash[:])
	if identity != "" {
		return identity + "|" + generation
	}
	// 仅在没有稳定身份时才使用 refresh token；access token 刻意不作为
	// singleflight key。
	return generation
}
