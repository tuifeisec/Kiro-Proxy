// router.go
// 路由层：根据 source model 找到对应的模型配置

package router

import (
	"kiro-proxy/store"
	"log"
	"strings"
	"sync"
)

// Route 一次路由决策的结果（直接就是 Model 配置）
type Route struct {
	Model *store.Model
}

// Router 维护内存路由表
type Router struct {
	db *store.DB

	mu     sync.RWMutex
	models []store.Model
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

	r.mu.Lock()
	r.models = models
	r.mu.Unlock()

	log.Printf("[router] 路由表已加载: %d 条模型配置", len(models))
}

// Resolve 根据 Kiro 请求中的 modelId 找到模型配置
// Kiro 发送的 modelId 实际上是"显示名称"(Name)，因此：
// 优先按 Name 精确匹配 → 回退按 ModelID 精确匹配 → 第一条可用模型（兜底）
func (r *Router) Resolve(modelID string) *Route {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var fallback *store.Model
	var modelIDMatch *store.Model

	for i := range r.models {
		m := &r.models[i]
		if !m.Enabled {
			continue
		}
		if fallback == nil {
			fallback = m
		}
		// 优先按显示名称匹配（Kiro 发来的就是 Name）
		name := m.Name
		if name == "" {
			name = m.ModelID
		}
		if name == modelID {
			return &Route{Model: m}
		}
		// 兼容：也按 model_id 匹配（向后兼容旧配置）
		if modelIDMatch == nil && m.ModelID == modelID {
			modelIDMatch = m
		}
	}

	// 回退到 ModelID 匹配
	if modelIDMatch != nil {
		return &Route{Model: modelIDMatch}
	}

	// 未找到精确匹配时，用第一条可用模型（让 classifier 等请求也能走通）
	if fallback != nil {
		return &Route{Model: fallback}
	}
	return nil
}

// ResolveByName 只按 Kiro 可见的显示名称(Name)解析模型。
// 用于 classifier “跟随主模型”场景：跟随的是用户在 Kiro 下拉框选择的模型名称，
// 不能回退到上游 ModelID 或第一条可用模型，否则同一 ModelID 的多个渠道会串路由。
func (r *Router) ResolveByName(modelName string) *Route {
	r.mu.RLock()
	defer r.mu.RUnlock()

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
			return &Route{Model: m}
		}
	}
	return nil
}

// EnabledModels 返回所有启用的模型（用于向 Kiro 返回模型列表）
func (r *Router) EnabledModels() []store.Model {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []store.Model
	for _, m := range r.models {
		if m.Enabled {
			result = append(result, m)
		}
	}
	return result
}

// ResolveClassifier 返回 classifier 专用模型配置
// 从 settings 表读取 classifier_model_id，优先按内部 ID 匹配；为兼容旧配置，
// 也允许填写 Kiro 可见的 Name 或上游 ModelID。
func (r *Router) ResolveClassifier() *Route {
	classifierID, _ := r.db.GetSetting("classifier_model_id")
	if classifierID == "" {
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
			return &Route{Model: m}
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
		return &Route{Model: nameMatch}
	}
	if modelIDMatch != nil {
		return &Route{Model: modelIDMatch}
	}
	return nil
}

// DetectProtocol 根据 host 和 path 判断请求协议类型
func DetectProtocol(host, path string) string {
	if isAWSHost(host) {
		switch {
		case hasSuffix(path, "/generateAssistantResponse"):
			return "kiro"
		case hasSuffix(path, "/ListAvailableModels"):
			return "kiro-models"
		case contains(path, "/getUsageLimits"):
			return "kiro-usage-limits"
		default:
			return "kiro-passthrough" // 其他 Kiro 官方请求放行到原始服务器
		}
	}
	return ""
}

func isAWSHost(host string) bool {
	h := strings.Split(host, ":")[0]
	return strings.HasSuffix(h, ".amazonaws.com")
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
