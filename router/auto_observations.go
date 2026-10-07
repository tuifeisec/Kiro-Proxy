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
	DefinitionID       string    `json:"definition_id"`
	DefinitionRevision string    `json:"definition_revision"`
	NodePath           string    `json:"node_path"`
	NodeID             string    `json:"node_id"`
	TaskType           string    `json:"task_type"`
	SelectedDuty       string    `json:"selected_duty"`
	SelectionSource    string    `json:"selection_source"`
	UnknownReason      string    `json:"unknown_reason"`
	TargetGroupID      string    `json:"target_group_id"`
	TargetGroupName    string    `json:"target_group_name"`
	ModelConfigID      string    `json:"model_config_id"`
	OutboundModel      string    `json:"outbound_model"`
	EffectiveEffort    string    `json:"effective_effort"`
	ChannelIdentity    string    `json:"channel_identity"`
	LockScope          string    `json:"lock_scope"`
	Count              uint64    `json:"count"`
	LastSeen           time.Time `json:"last_seen"`
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
	observation := AutoObservation{
		DefinitionID:       boundedObservationValue(conv.WorkflowDefinitionID, 1024),
		DefinitionRevision: boundedObservationValue(conv.WorkflowDefinitionRevision, 256),
		NodePath:           boundedObservationValue(conv.WorkflowNodePath, 1024),
		NodeID:             boundedObservationValue(conv.WorkflowNodeID, 128),
		TaskType:           boundedObservationValue(conv.WorkflowTaskType, 128),
		SelectedDuty:       boundedObservationValue(route.AutoDuty, 128),
		SelectionSource:    boundedObservationValue(route.AutoSelectionSource, 64),
		UnknownReason:      boundedObservationValue(route.AutoUnknownReason, 128),
		ModelConfigID:      boundedObservationValue(route.Model.ID, 256),
		OutboundModel:      boundedObservationValue(route.Model.ModelID, 256),
		EffectiveEffort:    boundedObservationValue(route.AutoEffort, 32),
		ChannelIdentity:    boundedObservationValue(route.AutoChannelIdentity, 64),
		LockScope:          boundedObservationValue(lockScope, 32),
		Count:              1,
		LastSeen:           time.Now().UTC(),
	}
	if route.Group != nil {
		observation.TargetGroupID = boundedObservationValue(route.Group.ID, 256)
		observation.TargetGroupName = boundedObservationValue(route.Group.Name, 256)
	}
	keyBytes, _ := json.Marshal([]string{observation.DefinitionID, observation.DefinitionRevision, observation.NodePath,
		observation.NodeID, observation.TaskType, observation.SelectedDuty, observation.SelectionSource, observation.UnknownReason,
		observation.TargetGroupID, observation.TargetGroupName, observation.ModelConfigID, observation.OutboundModel,
		observation.EffectiveEffort, observation.ChannelIdentity, observation.LockScope})
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
