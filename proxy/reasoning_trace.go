package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"net/http"
	"strings"
	"time"
)

type upstreamResponseMetadataKey struct{}

type upstreamResponseMetadata struct {
	Protocol             string
	CompatibilityHistory map[string]converter.ReasoningTrace
}

func markUpstreamResponse(resp *http.Response, protocol string, history map[string]converter.ReasoningTrace) {
	if resp == nil || resp.Request == nil {
		return
	}
	metadata := upstreamResponseMetadata{Protocol: protocol, CompatibilityHistory: history}
	ctx := context.WithValue(resp.Request.Context(), upstreamResponseMetadataKey{}, metadata)
	resp.Request = resp.Request.Clone(ctx)
}

func upstreamMetadataFromResponse(resp *http.Response) upstreamResponseMetadata {
	if resp == nil || resp.Request == nil {
		return upstreamResponseMetadata{}
	}
	metadata, _ := resp.Request.Context().Value(upstreamResponseMetadataKey{}).(upstreamResponseMetadata)
	return metadata
}

func reasoningProtocol(pre *Preflight, model *store.Model) string {
	if model == nil {
		return ""
	}
	if model.Provider == "anthropic" {
		return "anthropic"
	}
	if model.Provider == "codex" || useResponsesAPI(pre, model) {
		return "responses"
	}
	if model.Provider == "gemini" {
		return ""
	}
	return "chat"
}

func reasoningStateKeyFor(pre *Preflight, model *store.Model) string {
	if pre == nil || pre.Conv == nil || model == nil || pre.Conv.IsClassifier {
		return ""
	}
	base := responsesContinuationStateKey(pre.Conv)
	protocol := reasoningProtocol(pre, model)
	if base == "" || protocol == "" {
		return ""
	}
	parts := []string{
		base,
		strings.TrimSpace(model.ID),
		strings.TrimSpace(model.ModelID),
		strings.ToLower(strings.TrimSpace(model.Provider)),
		strings.TrimSpace(model.BaseURL),
		strings.TrimSpace(model.Endpoint),
		resolveReasoningEffort(pre, model),
		protocol,
	}
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte{byte(len(part) >> 24), byte(len(part) >> 16), byte(len(part) >> 8), byte(len(part))})
		hash.Write([]byte(part))
	}
	return "reasoning|" + hex.EncodeToString(hash.Sum(nil)[:16])
}

func (f *Forwarder) loadReasoningHistory(pre *Preflight, model *store.Model) (map[string]converter.ReasoningTrace, error) {
	stateKey := reasoningStateKeyFor(pre, model)
	if f.db == nil || stateKey == "" {
		return nil, nil
	}
	stored, err := f.db.ListReasoningTraces(stateKey)
	if err != nil || len(stored) == 0 {
		return nil, err
	}
	result := make(map[string]converter.ReasoningTrace, len(stored))
	for _, trace := range stored {
		result[trace.TurnKey] = converter.ReasoningTrace{
			Present: true,
			Text:    trace.Text, Signature: trace.Signature, ItemID: trace.ItemID,
			Status: trace.Status, WireField: trace.WireField, Protocol: trace.Protocol,
		}
	}
	return result, nil
}

func (f *Forwarder) saveReasoningTrace(pre *Preflight, model *store.Model, trace converter.ReasoningTrace) error {
	stateKey := reasoningStateKeyFor(pre, model)
	turnKey := converter.ReasoningTurnKey(trace.ToolCallIDs)
	if f.db == nil || stateKey == "" || turnKey == "" || strings.TrimSpace(trace.Text) == "" {
		return nil
	}
	return f.db.UpsertReasoningTrace(&store.ReasoningTrace{
		StateKey: stateKey, TurnKey: turnKey, Protocol: trace.Protocol,
		Text: trace.Text, Signature: trace.Signature, ItemID: trace.ItemID,
		Status: trace.Status, WireField: trace.WireField, UpdatedAt: time.Now(),
	})
}

func reasoningHistoryPrefersChat(history map[string]converter.ReasoningTrace) bool {
	for _, trace := range history {
		if trace.Protocol == "chat" {
			return true
		}
	}
	return false
}

func reasoningChatWireField(history map[string]converter.ReasoningTrace) string {
	for _, trace := range history {
		if trace.Protocol == "chat" && trace.WireField == "reasoning_content" {
			return "reasoning_content"
		}
	}
	for _, trace := range history {
		if trace.Protocol == "chat" && (trace.WireField == "reasoning_text" || trace.WireField == "reasoning") {
			return trace.WireField
		}
	}
	return "reasoning_content"
}

func reasoningHistoryWithWireField(history map[string]converter.ReasoningTrace, wireField string) map[string]converter.ReasoningTrace {
	result := make(map[string]converter.ReasoningTrace, len(history))
	for key, trace := range history {
		trace.Protocol = "chat"
		trace.WireField = wireField
		result[key] = trace
	}
	return result
}

func reasoningCompatibilityHistory(conv *converter.ParsedConversation, history map[string]converter.ReasoningTrace, wireField string) map[string]converter.ReasoningTrace {
	result := reasoningHistoryWithWireField(history, wireField)
	return reasoningHistoryFillMissing(conv, result, wireField)
}

func reasoningHistoryFillMissing(conv *converter.ParsedConversation, history map[string]converter.ReasoningTrace, wireField string) map[string]converter.ReasoningTrace {
	result := make(map[string]converter.ReasoningTrace, len(history))
	for key, trace := range history {
		result[key] = trace
	}
	if conv == nil {
		return result
	}
	for _, entry := range conv.History {
		if !entry.HasAssistant || len(entry.ToolUses) == 0 {
			continue
		}
		ids := make([]string, 0, len(entry.ToolUses))
		for _, tool := range entry.ToolUses {
			ids = append(ids, tool.ToolUseID)
		}
		turnKey := converter.ReasoningTurnKey(ids)
		if turnKey == "" {
			continue
		}
		if _, exists := result[turnKey]; !exists {
			result[turnKey] = converter.ReasoningTrace{Present: true, Protocol: "chat", WireField: wireField}
		}
	}
	return result
}

func hasAssistantToolTurns(conv *converter.ParsedConversation) bool {
	if conv == nil {
		return false
	}
	for _, entry := range conv.History {
		if entry.HasAssistant && len(entry.ToolUses) > 0 {
			return true
		}
	}
	return false
}

func (f *Forwarder) persistReasoningCompatibilityHistory(pre *Preflight, model *store.Model, history map[string]converter.ReasoningTrace) error {
	stateKey := reasoningStateKeyFor(pre, model)
	if f.db == nil || stateKey == "" {
		return nil
	}
	for turnKey, trace := range history {
		if turnKey == "" {
			continue
		}
		if err := f.db.UpsertReasoningTrace(&store.ReasoningTrace{
			StateKey: stateKey, TurnKey: turnKey, Protocol: "chat", Text: trace.Text,
			Signature: trace.Signature, ItemID: trace.ItemID, Status: trace.Status,
			WireField: trace.WireField, UpdatedAt: time.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func isMissingReasoningReplayError(status int, body string) bool {
	if status != 400 {
		return false
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "invalid_request_error") &&
		(strings.Contains(lower, "reasoning_text") || strings.Contains(lower, "reasoning_content")) &&
		strings.Contains(lower, "must be passed back")
}

func isResponsesDeveloperRoleRejected(status int, body string) bool {
	if status != 400 {
		return false
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "invalid_request") &&
		strings.Contains(lower, "message role must be one of") &&
		!strings.Contains(lower, "developer")
}

func reasoningWireFieldForError(header http.Header, body string) string {
	if strings.TrimSpace(header.Get("X-New-Api-Version")) != "" {
		return "reasoning_content"
	}
	lower := strings.ToLower(body)
	if strings.Contains(lower, "reasoning_content") {
		return "reasoning_content"
	}
	return "reasoning_text"
}
