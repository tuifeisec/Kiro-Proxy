package converter

import (
	"crypto/sha256"
	"encoding/hex"

	json "github.com/goccy/go-json"
)

// PrefixAuditShape captures only hashes and routing metadata needed to compare
// cache-sensitive request prefixes across turns.
type PrefixAuditShape struct {
	SystemHash    string
	ToolsHash     string
	CacheKey      string
	ModelConfigID string
	ModelID       string
	Provider      string
	Endpoint      string
	API           string
	ItemHashes    []string
}

type PrefixAuditDiagnostics struct {
	HasPrevious      bool
	PrefixChanged    bool
	InputRelation    string
	ChangeReasons    []string
	StableItems      int
	AddedItems       int
	RemovedItems     int
	FirstChangedItem int
}

// PrefixAuditItemHashes hashes each serialized message/input item separately.
// This preserves enough structure to distinguish append-only growth from a
// rewritten history without retaining request contents.
func PrefixAuditItemHashes(inputJSON string) ([]string, error) {
	return prefixAuditItemHashes(inputJSON, false)
}

// PrefixAuditItemHashesIgnoringCacheControl ignores Anthropic's rolling
// cache_control metadata while retaining the message content comparison.
func PrefixAuditItemHashesIgnoringCacheControl(inputJSON string) ([]string, error) {
	return prefixAuditItemHashes(inputJSON, true)
}

func prefixAuditItemHashes(inputJSON string, ignoreCacheControl bool) ([]string, error) {
	if inputJSON == "" {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(inputJSON), &items); err != nil {
		return nil, err
	}
	hashes := make([]string, len(items))
	for i, item := range items {
		content := []byte(item)
		if ignoreCacheControl {
			var value any
			if err := json.Unmarshal(item, &value); err != nil {
				return nil, err
			}
			removePrefixAuditCacheControl(value)
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			content = encoded
		}
		sum := sha256.Sum256(content)
		hashes[i] = hex.EncodeToString(sum[:8])
	}
	return hashes, nil
}

func removePrefixAuditCacheControl(value any) {
	switch value := value.(type) {
	case map[string]any:
		delete(value, "cache_control")
		for _, child := range value {
			removePrefixAuditCacheControl(child)
		}
	case []any:
		for _, child := range value {
			removePrefixAuditCacheControl(child)
		}
	}
}

// ComparePrefixAuditShapes explains whether the current provider input is an
// append-only continuation or whether a cache-sensitive prefix was rewritten.
func ComparePrefixAuditShapes(previous *PrefixAuditShape, current PrefixAuditShape) PrefixAuditDiagnostics {
	diagnostics := PrefixAuditDiagnostics{
		InputRelation:    "initial",
		FirstChangedItem: -1,
	}
	if previous == nil {
		return diagnostics
	}
	diagnostics.HasPrevious = true

	compareField := func(previousValue, currentValue, reason string) {
		if previousValue != currentValue {
			diagnostics.ChangeReasons = append(diagnostics.ChangeReasons, reason)
		}
	}
	compareField(previous.SystemHash, current.SystemHash, "system")
	compareField(previous.ToolsHash, current.ToolsHash, "tools")
	compareField(previous.CacheKey, current.CacheKey, "cache_key")
	compareField(previous.ModelConfigID, current.ModelConfigID, "model_config")
	compareField(previous.ModelID, current.ModelID, "model")
	compareField(previous.Provider, current.Provider, "provider")
	compareField(previous.Endpoint, current.Endpoint, "endpoint")
	compareField(previous.API, current.API, "api")

	if previous.ItemHashes == nil || current.ItemHashes == nil {
		diagnostics.InputRelation = "unavailable"
		diagnostics.PrefixChanged = len(diagnostics.ChangeReasons) > 0
		return diagnostics
	}

	common := commonPrefixHashes(previous.ItemHashes, current.ItemHashes)
	diagnostics.StableItems = common
	if len(current.ItemHashes) > len(previous.ItemHashes) {
		diagnostics.AddedItems = len(current.ItemHashes) - len(previous.ItemHashes)
	} else if len(previous.ItemHashes) > len(current.ItemHashes) {
		diagnostics.RemovedItems = len(previous.ItemHashes) - len(current.ItemHashes)
	}

	switch {
	case common == len(previous.ItemHashes) && common == len(current.ItemHashes):
		diagnostics.InputRelation = "same"
	case common == len(previous.ItemHashes):
		diagnostics.InputRelation = "append"
	case common == len(current.ItemHashes):
		diagnostics.InputRelation = "shortened"
		diagnostics.FirstChangedItem = common
		diagnostics.ChangeReasons = append(diagnostics.ChangeReasons, "history_shortened")
	default:
		diagnostics.InputRelation = "rewrite"
		diagnostics.FirstChangedItem = common
		diagnostics.ChangeReasons = append(diagnostics.ChangeReasons, "history_rewrite")
	}
	diagnostics.PrefixChanged = len(diagnostics.ChangeReasons) > 0
	return diagnostics
}

func commonPrefixHashes(previous, current []string) int {
	limit := len(previous)
	if len(current) < limit {
		limit = len(current)
	}
	for i := 0; i < limit; i++ {
		if previous[i] != current[i] {
			return i
		}
	}
	return limit
}
