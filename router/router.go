// router.go
// 路由层：根据 source model 找到对应的模型配置

package router

import (
	"kiro-proxy/store"
	"log"
	"strings"
	"sync"
)

// Route 一次路由决策的结果。未分组模型只有一个候选。
type Route struct {
	Model           *store.Model
	Candidates      []*store.Model
	CandidateLimits map[string]int
	CandidateGroups map[string]*store.ModelGroup
	Group           *store.ModelGroup
	RequestedName   string
}

// Router 维护内存路由表
type Router struct {
	db *store.DB

	mu     sync.RWMutex
	models []store.Model
	groups []store.ModelGroup
}

// New 创建路由器并加载初始数据
func New(db *store.DB) *Router {
	r := &Router{db: db}
	r.Reload()
	return r
}

// Reload 从数据库重新加载路由表
func (r *Router) Reload() {
	models, err := r.db.GetModels()
	if err != nil {
		log.Printf("[router] 加载模型失败: %v", err)
		return
	}
	groups, err := r.db.GetModelGroups()
	if err != nil {
		log.Printf("[router] 加载模型分组失败: %v", err)
		return
	}

	r.mu.Lock()
	r.models = models
	r.groups = groups
	r.mu.Unlock()

	log.Printf("[router] 路由表已加载: %d 条模型配置, %d 个分组", len(models), len(groups))
}

// Resolve 根据 Kiro 请求中的 modelId 找到模型配置
// Kiro 发送的 modelId 实际上是"显示名称"(Name)，因此：
// 优先按 Name 精确匹配 → 回退按 ModelID 精确匹配 → 第一条可用模型（兜底）
func (r *Router) Resolve(modelID string) *Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if route := r.resolveGroupByNameLocked(modelID); route != nil {
		return route
	}

	var fallback *Route
	var modelIDMatch *Route

	for i := range r.models {
		m := &r.models[i]
		if !m.Enabled {
			continue
		}
		route := r.routeForModelLocked(m, modelID)
		if route == nil {
			continue
		}
		if fallback == nil {
			fallback = route
		}
		// 优先按显示名称匹配（Kiro 发来的就是 Name）
		name := m.Name
		if name == "" {
			name = m.ModelID
		}
		if name == modelID {
			return route
		}
		// 兼容：也按 model_id 匹配（向后兼容旧配置）
		if modelIDMatch == nil && m.ModelID == modelID {
			modelIDMatch = route
		}
	}

	// 回退到 ModelID 匹配
	if modelIDMatch != nil {
		return modelIDMatch
	}

	// 未找到精确匹配时，用第一条可用模型（让 classifier 等请求也能走通）
	if fallback != nil {
		return fallback
	}
	return nil
}

// ResolveByName 只按 Kiro 可见的显示名称(Name)解析模型。
// 用于 classifier “跟随主模型”场景：跟随的是用户在 Kiro 下拉框选择的模型名称，
// 不能回退到上游 ModelID 或第一条可用模型，否则同一 ModelID 的多个渠道会串路由。
func (r *Router) ResolveByName(modelName string) *Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if route := r.resolveGroupByNameLocked(modelName); route != nil {
		return route
	}

	for i := range r.models {
		m := &r.models[i]
		if !m.Enabled {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ModelID
		}
		if name == modelName {
			return r.routeForModelLocked(m, modelName)
		}
	}
	return nil
}

// ResolveExact 根据模型名称或上游 model_id 精确解析模型，不做兜底回退。
// 用于本地 OpenAI/Anthropic 兼容接口，避免未知 model 被误路由到第一条可用模型。
func (r *Router) ResolveExact(modelID string) *Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if route := r.resolveGroupByNameLocked(modelID); route != nil {
		return route
	}

	var modelIDMatch *Route
	for i := range r.models {
		m := &r.models[i]
		if !m.Enabled {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ModelID
		}
		if name == modelID {
			return r.routeForModelLocked(m, modelID)
		}
		if modelIDMatch == nil && m.ModelID == modelID {
			modelIDMatch = r.routeForModelLocked(m, modelID)
		}
	}
	if modelIDMatch != nil {
		return modelIDMatch
	}
	return nil
}

// EnabledModels 返回所有启用的模型（用于向 Kiro 返回模型列表）
func (r *Router) EnabledModels() []store.Model {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []store.Model
	for i := range r.groups {
		group := &r.groups[i]
		if !group.Enabled {
			continue
		}
		route := r.routeForGroupLocked(group, group.Name)
		if route == nil || route.Model == nil {
			continue
		}
		virtual := *route.Model
		virtual.ID = group.ID
		virtual.Name = group.Name
		result = append(result, virtual)
	}
	for _, m := range r.models {
		if m.Enabled {
			result = append(result, m)
		}
	}
	return result
}

// ResolveByID 按内部配置 ID 精确解析单个模型，不展开所属分组。
func (r *Router) ResolveByID(id string) *Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.models {
		m := &r.models[i]
		if m.Enabled && m.ID == id {
			return r.routeForModelLocked(m, id)
		}
	}
	return nil
}

func (r *Router) resolveGroupByNameLocked(name string) *Route {
	for i := range r.groups {
		group := &r.groups[i]
		if group.Enabled && group.Name == name {
			return r.routeForGroupLocked(group, name)
		}
	}
	return nil
}

func (r *Router) routeForModelLocked(model *store.Model, requestedName string) *Route {
	return singleModelRoute(model, requestedName, r.modelConcurrencyLimitLocked(model.ID))
}

func (r *Router) modelConcurrencyLimitLocked(modelID string) int {
	for i := range r.groups {
		if !r.groups[i].Enabled {
			continue
		}
		for _, member := range r.groups[i].Members {
			if member.ModelID == modelID {
				return member.MaxConcurrency
			}
		}
	}
	return 0
}

func (r *Router) routeForGroupLocked(group *store.ModelGroup, requestedName string) *Route {
	if group == nil || !group.Enabled {
		return nil
	}
	candidates := make([]*store.Model, 0, len(group.Members))
	limits := make(map[string]int)
	candidateGroups := make(map[string]*store.ModelGroup)
	r.collectGroupCandidatesLocked(group, make(map[string]bool), make(map[string]bool), &candidates, limits, candidateGroups)
	if len(candidates) == 0 {
		return nil
	}
	return &Route{
		Model:           candidates[0],
		Candidates:      candidates,
		CandidateLimits: limits,
		CandidateGroups: candidateGroups,
		Group:           group,
		RequestedName:   requestedName,
	}
}

func (r *Router) collectGroupCandidatesLocked(
	group *store.ModelGroup,
	visitedGroups map[string]bool,
	seenModels map[string]bool,
	candidates *[]*store.Model,
	limits map[string]int,
	candidateGroups map[string]*store.ModelGroup,
) {
	if group == nil || !group.Enabled || visitedGroups[group.ID] {
		return
	}
	visitedGroups[group.ID] = true
	for _, member := range group.Members {
		if seenModels[member.ModelID] {
			continue
		}
		for i := range r.models {
			model := &r.models[i]
			if model.ID == member.ModelID && model.Enabled {
				*candidates = append(*candidates, model)
				limits[model.ID] = member.MaxConcurrency
				candidateGroups[model.ID] = group
				seenModels[model.ID] = true
				break
			}
		}
	}
	if group.FallbackGroupID == "" {
		return
	}
	for i := range r.groups {
		if r.groups[i].ID == group.FallbackGroupID {
			r.collectGroupCandidatesLocked(&r.groups[i], visitedGroups, seenModels, candidates, limits, candidateGroups)
			return
		}
	}
}

func singleModelRoute(model *store.Model, requestedName string, maxConcurrency int) *Route {
	if model == nil {
		return nil
	}
	return &Route{
		Model: model, Candidates: []*store.Model{model},
		CandidateLimits: map[string]int{model.ID: maxConcurrency}, CandidateGroups: map[string]*store.ModelGroup{},
		RequestedName: requestedName,
	}
}

// ResolveClassifier 返回 classifier 专用模型配置
// 从 settings 表读取 classifier_model_id，优先按内部 ID 匹配；为兼容旧配置，
// 也允许填写 Kiro 可见的 Name 或上游 ModelID。
func (r *Router) GetClassifierSetting() (string, error) {
	return r.db.GetSetting("classifier_model_id")
}

func (r *Router) ResolveClassifier() *Route {
	classifierID, _ := r.GetClassifierSetting()
	if classifierID == "" || IsClassifierDisabledSetting(classifierID) {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var nameMatch *store.Model
	var modelIDMatch *store.Model
	for i := range r.models {
		m := &r.models[i]
		if !m.Enabled {
			continue
		}
		if m.ID == classifierID {
			return r.routeForModelLocked(m, classifierID)
		}

		name := m.Name
		if name == "" {
			name = m.ModelID
		}
		if nameMatch == nil && name == classifierID {
			nameMatch = m
		}
		if modelIDMatch == nil && m.ModelID == classifierID {
			modelIDMatch = m
		}
	}

	if nameMatch != nil {
		return r.routeForModelLocked(nameMatch, classifierID)
	}
	if modelIDMatch != nil {
		return r.routeForModelLocked(modelIDMatch, classifierID)
	}
	return nil
}

// DetectProtocol 根据 host 和 path 判断请求协议类型
func DetectProtocol(host, path string) string {
	hostname := normalizeHost(host)

	// 旧版本：Kiro 通过 AWS CodeWhisperer 域名访问。
	if isAWSHostName(hostname) {
		switch {
		case hasSuffix(path, "/generateAssistantResponse"):
			return "kiro"
		case hasSuffix(path, "/ListAvailableModels"):
			return "kiro-models"
		case contains(path, "/getUsageLimits"):
			return "kiro-usage-limits"
		default:
			return "kiro-passthrough"
		}
	}

	// 新版本：Kiro 按职责拆分为 runtime / management 子域名。
	if isKiroRuntimeHostName(hostname) {
		switch {
		case path == "/":
			// 新版将对话操作放到根路径，请求层需继续根据 method/body 确认。
			return "kiro-runtime-root"
		case hasSuffix(path, "/generateAssistantResponse"):
			return "kiro"
		default:
			return "kiro-passthrough"
		}
	}

	if isKiroManagementHostName(hostname) {
		switch {
		case path == "/":
			return "kiro-models"
		case contains(path, "/getUsageLimits"):
			return "kiro-usage-limits"
		default:
			return "kiro-passthrough"
		}
	}

	return ""
}

func normalizeHost(host string) string {
	return strings.Split(host, ":")[0]
}

func isAWSHostName(host string) bool {
	return strings.HasSuffix(host, ".amazonaws.com")
}

func isKiroRuntimeHostName(host string) bool {
	return host == "runtime.kiro.dev" || strings.HasPrefix(host, "runtime.") && strings.HasSuffix(host, ".kiro.dev")
}

func isKiroManagementHostName(host string) bool {
	return host == "management.kiro.dev" || strings.HasPrefix(host, "management.") && strings.HasSuffix(host, ".kiro.dev")
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
