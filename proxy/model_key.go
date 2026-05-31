package proxy

import "kiro-proxy/store"

// statsModelID returns the Kiro-visible model identity used for per-model stats.
// Name is what Kiro sends back as modelId, while ModelID is only the upstream
// model field. Using Name prevents different configured routes that share the
// same upstream model identifier from being merged in usage details.
func statsModelID(m *store.Model) string {
	if m == nil {
		return ""
	}
	if m.Name != "" {
		return m.Name
	}
	return m.ModelID
}
