package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	json "github.com/goccy/go-json"
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"strings"
)

func responsesSessionScope(base string, conv *converter.ParsedConversation, model *store.Model, effort string) string {
	fields := []interface{}{"responses-instructions-v2", strings.TrimSpace(effort)}
	if model != nil {
		identity := model.APIKey
		if model.Provider == "codex" {
			identity = model.CodexAccountID
			if identity == "" {
				identity = model.CodexEmail
			}
			if identity == "" {
				identity = model.RefreshToken
			}
		}
		credentialHash := sha256.Sum256([]byte(identity))
		fields = append(fields, model.ID, model.Provider, model.ModelID, strings.TrimRight(model.BaseURL, "/"), model.Endpoint, model.DisableAutoSuffix, model.ServiceTier, hex.EncodeToString(credentialHash[:]))
	}
	if conv != nil {
		static, _ := converter.OptimizeSystemForCacheForAudit(conv.SystemText)
		fields = append(fields, static, conv.ToolsHash)
		if conv.ToolsHash == "" && len(conv.RawTools) > 0 {
			fields = append(fields, conv.RawTools)
		}
	}
	encoded, _ := json.Marshal(fields)
	hash := sha256.Sum256(encoded)
	return base + "|v2:" + hex.EncodeToString(hash[:])
}
