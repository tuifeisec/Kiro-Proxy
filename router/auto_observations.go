package router

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"kiro-proxy/converter"
)

const maxAutoObservations = 512

// AutoObservation is a bounded, in-memory routing record. It contains no prompt
// content or credentials.
type AutoObservation struct {
	WorkflowMetadataVersion *int      `json:"workflow_metadata_version,omitempty"`
	WorkflowStage           string    `json:"workflow_stage"`
	WorkflowStageSource     string    `json:"workflow_stage_source"`
	WorkflowStageStatus     string    `json:"workflow_stage_status"`
	WorkflowStageProvided   *bool     `json:"workflow_stage_provided,omitempty"`
	SelectedStage           string    `json:"selected_stage"`
	WorkflowID              string    `json:"workflow_id"`
	AgentTaskType           string    `json:"agent_task_type"`
	AgentMode               string    `json:"agent_mode"`
	ChatTriggerType         string    `json:"chat_trigger_type"`
	ConversationID          string    `json:"conversation_id"`
	ExecutionID             string    `json:"execution_id"`
	DefinitionID            string    `json:"definition_id"`
	DefinitionRevision      string    `json:"definition_revision"`
	NodePath                string    `json:"node_path"`
	NodeID                  string    `json:"node_id"`
	TaskType                string    `json:"task_type"`
	SelectedDuty            string    `json:"selected_duty"`
	SelectionSource         string    `json:"selection_source"`
	UnknownReason           string    `json:"unknown_reason"`
	TargetGroupID           string    `json:"target_group_id"`
	TargetGroupName         string    `json:"target_group_name"`
	ModelConfigID           string    `json:"model_config_id"`
	OutboundModel           string    `json:"outbound_model"`
	EffectiveEffort         string    `json:"effective_effort"`
	ChannelIdentity         string    `json:"channel_identity"`
	LockScope               string    `json:"lock_scope"`
	Count                   uint64    `json:"count"`
	LastSeen                time.Time `json:"last_seen"`
}

// GetAutoObservations returns the current process's bounded routing snapshot.
func (r *Router) GetAutoObservations() []AutoObservation {
	if r == nil {
		return []AutoObservation{}
	}
	r.autoMu.Lock()
	defer r.autoMu.Unlock()
	result := make([]AutoObservation, 0, len(r.autoObservations))
	for _, observation := range r.autoObservations {
		result = append(result, observation)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].LastSeen.Equal(result[j].LastSeen) {
			if result[i].DefinitionID != result[j].DefinitionID {
				return result[i].DefinitionID < result[j].DefinitionID
			}
			if result[i].DefinitionRevision != result[j].DefinitionRevision {
				return result[i].DefinitionRevision < result[j].DefinitionRevision
			}
			return result[i].NodePath < result[j].NodePath
		}
		return result[i].LastSeen.After(result[j].LastSeen)
	})
	return result
}

// RecordAutoDispatch records a group Auto request after forwarding selected the
// actual candidate in route.Model. Single-model requests are recorded at resolve.
func (r *Router) RecordAutoDispatch(conv *converter.ParsedConversation, route *Route) {
	if r == nil || conv == nil || route == nil || route.Model == nil || route.Group == nil || route.AutoMode == "" {
		return
	}
	r.autoMu.Lock()
	defer r.autoMu.Unlock()
	route.AutoChannelIdentity = autoChannelIdentity(route.Model)
	r.recordAutoObservationLocked(conv, route, route.AutoLockScope)
}

// Caller holds autoMu.
func (r *Router) recordAutoObservationLocked(conv *converter.ParsedConversation, route *Route, lockScope string) {
	if conv == nil || route == nil || route.Model == nil {
		return
	}
	if route.Group != nil && route.AutoChannelIdentity == "" {
		return // The initial model is only a candidate until dispatch.
	}
	version, provided := conv.WorkflowMetadataVersion, conv.WorkflowStageProvided
	observation := AutoObservation{
		WorkflowMetadataVersion: &version,
		WorkflowStage:           boundedObservationValue(conv.WorkflowStage, 128),
		WorkflowStageSource:     boundedObservationValue(conv.WorkflowStageSource, 128),
		WorkflowStageStatus:     autoObservationStageStatus(conv),
		WorkflowStageProvided:   &provided,
		SelectedStage:           boundedObservationValue(route.AutoStage, 128),
		WorkflowID:              boundedObservationValue(conv.WorkflowID, 256),
		AgentTaskType:           boundedObservationValue(conv.AgentTaskType, 128),
		AgentMode:               boundedObservationValue(conv.AgentMode, 128),
		ChatTriggerType:         boundedObservationValue(conv.ChatTriggerType, 128),
		ConversationID:          boundedObservationValue(conv.ConversationID, 256),
		ExecutionID:             boundedObservationValue(route.AutoExecutionID, 256),
		DefinitionID:            boundedObservationValue(conv.WorkflowDefinitionID, 1024),
		DefinitionRevision:      boundedObservationValue(conv.WorkflowDefinitionRevision, 256),
		NodePath:                boundedObservationValue(conv.WorkflowNodePath, 1024),
		NodeID:                  boundedObservationValue(conv.WorkflowNodeID, 128),
		TaskType:                boundedObservationValue(conv.WorkflowTaskType, 128),
		SelectedDuty:            boundedObservationValue(route.AutoDuty, 128),
		SelectionSource:         boundedObservationValue(route.AutoSelectionSource, 64),
		UnknownReason:           boundedObservationValue(route.AutoUnknownReason, 128),
		ModelConfigID:           boundedObservationValue(route.Model.ID, 256),
		OutboundModel:           boundedObservationValue(route.Model.ModelID, 256),
		EffectiveEffort:         boundedObservationValue(route.AutoEffort, 32),
		ChannelIdentity:         boundedObservationValue(route.AutoChannelIdentity, 64),
		LockScope:               boundedObservationValue(lockScope, 32),
		Count:                   1,
		LastSeen:                time.Now().UTC(),
	}
	if route.Group != nil {
		observation.TargetGroupID = boundedObservationValue(route.Group.ID, 256)
		observation.TargetGroupName = boundedObservationValue(route.Group.Name, 256)
	}
	// 元数据使用独立标量副本，后续请求修改不会改变观察快照。
	keyObservation := observation
	keyObservation.Count, keyObservation.LastSeen = 0, time.Time{}
	keyBytes, _ := json.Marshal(keyObservation)
	key := string(keyBytes)
	if r.autoObservations == nil {
		r.autoObservations = make(map[string]AutoObservation)
	}
	if existing, ok := r.autoObservations[key]; ok {
		if existing.Count < ^uint64(0) {
			existing.Count++
		}
		existing.LastSeen = observation.LastSeen
		r.autoObservations[key] = existing
		return
	}
	if len(r.autoObservations) >= maxAutoObservations {
		oldestKey := ""
		var oldest time.Time
		for candidateKey, candidate := range r.autoObservations {
			if oldestKey == "" || candidate.LastSeen.Before(oldest) {
				oldestKey, oldest = candidateKey, candidate.LastSeen
			}
		}
		delete(r.autoObservations, oldestKey)
	}
	r.autoObservations[key] = observation
}

func autoObservationStageStatus(conv *converter.ParsedConversation) string {
	if !conv.WorkflowStageProvided {
		return "unavailable"
	}
	if conv.WorkflowMetadataVersion == 2 {
		if conv.WorkflowTaskType == "" {
			return "unknown"
		}
		return "declared"
	}
	switch conv.WorkflowStage {
	case "run", "plan", "implement", "review", "finalize":
		return "recognized"
	default:
		return "unknown"
	}
}

func boundedObservationValue(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}
