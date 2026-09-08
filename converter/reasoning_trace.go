package converter

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// ReasoningTurnKey identifies one assistant tool-call turn without depending
// on visible assistant text, which Kiro may normalize between requests.
func ReasoningTurnKey(toolCallIDs []string) string {
	if len(toolCallIDs) == 0 {
		return ""
	}
	unique := make(map[string]struct{}, len(toolCallIDs))
	ids := make([]string, 0, len(toolCallIDs))
	for _, id := range toolCallIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := unique[id]; exists {
			continue
		}
		unique[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)

	var key strings.Builder
	for _, id := range ids {
		key.WriteString(strconv.Itoa(len(id)))
		key.WriteByte(':')
		key.WriteString(id)
	}
	sum := sha256.Sum256([]byte(key.String()))
	return hex.EncodeToString(sum[:16])
}

func appendUniqueToolCallID(ids []string, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ids
	}
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

func historyEntryReasoningTurnKey(entry ParsedHistoryEntry) string {
	ids := make([]string, 0, len(entry.ToolUses))
	for _, tool := range entry.ToolUses {
		ids = append(ids, tool.ToolUseID)
	}
	return ReasoningTurnKey(ids)
}

// WithReasoningHistory returns a shallow conversation copy with reasoning
// sidecars attached to matching assistant tool-call turns.
func WithReasoningHistory(conv *ParsedConversation, traces map[string]ReasoningTrace) *ParsedConversation {
	if conv == nil || len(traces) == 0 {
		return conv
	}
	clone := *conv
	clone.History = append([]ParsedHistoryEntry(nil), conv.History...)
	for i := range clone.History {
		turnKey := historyEntryReasoningTurnKey(clone.History[i])
		trace, ok := traces[turnKey]
		if !ok || turnKey == "" || (!trace.Present && strings.TrimSpace(trace.Text) == "") {
			continue
		}
		traceCopy := trace
		traceCopy.ToolCallIDs = append([]string(nil), trace.ToolCallIDs...)
		clone.History[i].Reasoning = &traceCopy
	}
	return &clone
}
