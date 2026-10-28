package proxy

import (
	"bytes"
	"io"
	"strings"

	json "github.com/goccy/go-json"
	"kiro-proxy/store"
)

// setResponsesStoreAndServiceTier applies the two top-level mutations used by
// the ordinary Kiro Responses path in one JSON round trip.  It deliberately
// keeps the legacy decoder mode: non-OpenAI Responses endpoints use the old
// float64 normalization, while native OpenAI Responses keeps json.Number.
//
// Continuation is applied after this helper and raw Responses preparation has
// its own wire-preservation contract, so neither path should call this helper.
func setResponsesStoreAndServiceTier(body []byte, storeValue, preserveNumbers bool, model *store.Model) []byte {
	var root map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if preserveNumbers {
		decoder.UseNumber()
	}
	if err := decoder.Decode(&root); err != nil || root == nil {
		return body
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return body
	}

	root["store"] = storeValue
	if shouldFillConfiguredResponsesPriority(root, model) {
		root["service_tier"] = "priority"
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return encoded
}

func shouldFillConfiguredResponsesPriority(root map[string]interface{}, model *store.Model) bool {
	if model == nil || !strings.EqualFold(strings.TrimSpace(model.Provider), "openai") || strings.ToLower(strings.TrimSpace(model.ServiceTier)) != "fast" {
		return false
	}
	tier, exists := root["service_tier"]
	if !exists || tier == nil {
		return true
	}
	tierString, ok := tier.(string)
	return ok && strings.EqualFold(strings.TrimSpace(tierString), "default")
}
