package proxy

import (
	"sync"
	"time"

	"kiro-proxy/converter"
)

const (
	prefixAuditTTL      = 30 * time.Minute
	maxPrefixAuditItems = 512
)

type prefixAuditEntry struct {
	shape    converter.PrefixAuditShape
	lastSeen time.Time
}

type prefixAuditTracker struct {
	mu      sync.Mutex
	entries map[string]prefixAuditEntry
}

func newPrefixAuditTracker() *prefixAuditTracker {
	return &prefixAuditTracker{entries: make(map[string]prefixAuditEntry)}
}

func (t *prefixAuditTracker) compareAndStore(key string, shape converter.PrefixAuditShape) converter.PrefixAuditDiagnostics {
	if t == nil || key == "" {
		return converter.ComparePrefixAuditShapes(nil, shape)
	}

	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()

	for entryKey, entry := range t.entries {
		if now.Sub(entry.lastSeen) > prefixAuditTTL {
			delete(t.entries, entryKey)
		}
	}

	var previous *converter.PrefixAuditShape
	if entry, ok := t.entries[key]; ok {
		previousShape := entry.shape
		previous = &previousShape
	}
	if previous == nil && len(t.entries) >= maxPrefixAuditItems {
		oldestKey := ""
		var oldestTime time.Time
		for entryKey, entry := range t.entries {
			if oldestKey == "" || entry.lastSeen.Before(oldestTime) {
				oldestKey = entryKey
				oldestTime = entry.lastSeen
			}
		}
		delete(t.entries, oldestKey)
	}

	diagnostics := converter.ComparePrefixAuditShapes(previous, shape)
	t.entries[key] = prefixAuditEntry{shape: shape, lastSeen: now}
	return diagnostics
}
