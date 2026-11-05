package proxy

import (
	"strings"
	"sync"
	"time"

	"kiro-proxy/store"
)

// 模型能力观测：记录上游对某个请求形态的明确拒绝，用于下一次请求发送前直接改写。
// key 为 (modelHealthKey, 上游协议, 能力类型)，模型被编辑后 UpdatedAt 变化，旧观测自动失效；
// 观测带 TTL，过期后重新按原形态探测。配置已显式决定的能力不应用、也不记录观测。
const modelCapabilityTTL = imageCapabilityTTL

type compatProtocol string

const (
	compatProtocolChat      compatProtocol = "chat"
	compatProtocolResponses compatProtocol = "responses"
	compatProtocolAnthropic compatProtocol = "anthropic"
	compatProtocolGemini    compatProtocol = "gemini"
	compatProtocolCodex     compatProtocol = "codex"
)

type modelCapabilityKind string

const (
	capabilityThinkingEnabled  modelCapabilityKind = "thinking_enabled"
	capabilityThinkingAdaptive modelCapabilityKind = "thinking_adaptive"
	capabilityThinkingDisplay  modelCapabilityKind = "thinking_display"
	capabilityReasoningEffort  modelCapabilityKind = "reasoning_effort"
	capabilityReasoningField   modelCapabilityKind = "reasoning_field"
	capabilityContinuation     modelCapabilityKind = "continuation"
	capabilityDeveloperRole    modelCapabilityKind = "developer_role"
	capabilityTokenField       modelCapabilityKind = "token_field"
	capabilitySampling         modelCapabilityKind = "sampling"
)

type modelCapabilityKey struct {
	model    string
	protocol compatProtocol
	kind     modelCapabilityKind
}

type modelCapabilityObservation struct {
	value     string
	evidence  string
	expiresAt time.Time
}

type modelCapabilityStore struct {
	mu      sync.Mutex
	entries map[modelCapabilityKey]modelCapabilityObservation
}

func newModelCapabilityStore() *modelCapabilityStore {
	return &modelCapabilityStore{entries: make(map[modelCapabilityKey]modelCapabilityObservation)}
}

func (s *modelCapabilityStore) configuredBy(model *store.Model, kind modelCapabilityKind) bool {
	return modelCapabilityConfigured(model, kind)
}

// lookup 返回未过期的观测；配置已决定该能力时始终视为没有观测。
func (s *modelCapabilityStore) lookup(model *store.Model, protocol compatProtocol, kind modelCapabilityKind, now time.Time) (modelCapabilityObservation, bool) {
	if s == nil || model == nil || s.configuredBy(model, kind) {
		return modelCapabilityObservation{}, false
	}
	key := modelCapabilityKey{model: modelHealthKey(model), protocol: protocol, kind: kind}
	s.mu.Lock()
	defer s.mu.Unlock()
	observation, ok := s.entries[key]
	if !ok {
		return modelCapabilityObservation{}, false
	}
	if !now.Before(observation.expiresAt) {
		delete(s.entries, key)
		return modelCapabilityObservation{}, false
	}
	return observation, true
}

// observe 记录一次已被重试成功验证的观测，返回是否实际写入。
func (s *modelCapabilityStore) observe(model *store.Model, protocol compatProtocol, kind modelCapabilityKind, value, evidence string, now time.Time) bool {
	if s == nil || model == nil || strings.TrimSpace(value) == "" || s.configuredBy(model, kind) {
		return false
	}
	key := modelCapabilityKey{model: modelHealthKey(model), protocol: protocol, kind: kind}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 编辑后的旧 key 不会再被查询，写入时顺带清理过期项，避免无界增长。
	for existing, observation := range s.entries {
		if !now.Before(observation.expiresAt) {
			delete(s.entries, existing)
		}
	}
	s.entries[key] = modelCapabilityObservation{value: value, evidence: evidence, expiresAt: now.Add(modelCapabilityTTL)}
	return true
}
