// api/settings.go
// 全局设置读写接口

package api

import (
	"fmt"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"net/http"
	"strings"

	json "github.com/goccy/go-json"
)

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.db.GetAllSettings()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		delete(settings, store.AutoRoutingStageFixedBackupSetting)
		typesafeConfig, err := store.ParseTypesafeClassifierConfig(settings[store.TypesafeClassifierSetting])
		if err != nil {
			writeError(w, http.StatusInternalServerError, safeTypesafeConfigError(settings[store.TypesafeClassifierSetting], err))
			return
		}
		if typesafeConfig.APIKey != "" {
			typesafeConfig.APIKey = store.TypesafeAPIKeyMask
		}
		normalizedTypesafeConfig, err := json.Marshal(typesafeConfig)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		settings[store.TypesafeClassifierSetting] = string(normalizedTypesafeConfig)
		writeOK(w, settings)

	case http.MethodPut:
		var body map[string]string
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if _, supplied := body[store.AutoRoutingStageFixedBackupSetting]; supplied {
			writeError(w, http.StatusBadRequest, store.AutoRoutingStageFixedBackupSetting+" is reserved for the server")
			return
		}
		if raw, ok := body[store.AutoRoutingSetting]; ok {
			config, err := store.ParseAutoRoutingConfig(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if config.Mode != store.AutoRoutingModeDutyFixed {
				writeError(w, http.StatusBadRequest, "stage_fixed is read-only; explicitly migrate and save duty_fixed v2")
				return
			}
			models, err := s.db.GetModels()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := router.ValidateAutoRoutingConfig(config, models); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			normalized, err := json.Marshal(config)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			body[store.AutoRoutingSetting] = string(normalized)
		}
		if raw, ok := body[store.TypesafeClassifierSetting]; ok {
			normalized, err := prepareTypesafeConfigForWrite(s.db, raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, safeTypesafeConfigError(raw, err))
				return
			}
			body[store.TypesafeClassifierSetting] = normalized
		}
		var saveErr error
		if _, writesAuto := body[store.AutoRoutingSetting]; writesAuto {
			saveErr = s.db.SetSettingsWithAutoRoutingMigration(body)
		} else {
			saveErr = s.db.SetSettings(body)
		}
		if saveErr != nil {
			writeError(w, http.StatusInternalServerError, saveErr.Error())
			return
		}
		// 如果修改了 active_mapping_id，同步更新路由器
		if id, ok := body["active_model_id"]; ok {
			_ = id // 不再需要 active model 概念
		}
		if _, ok := body[store.AutoRoutingSetting]; ok {
			s.router.Reload()
		}
		writeOK(w, map[string]bool{"ok": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func prepareTypesafeConfigForWrite(db *store.DB, raw string) (string, error) {
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", fmt.Errorf("invalid configuration: %w", err)
	}
	if keyRaw, ok := fields["api_key"]; ok {
		var requestedKey string
		if err := json.Unmarshal(keyRaw, &requestedKey); err != nil {
			return "", fmt.Errorf("invalid api_key: %w", err)
		}
		if requestedKey == store.TypesafeAPIKeyMask {
			storedRaw, err := db.GetSetting(store.TypesafeClassifierSetting)
			if err != nil {
				return "", err
			}
			storedConfig, err := store.ParseTypesafeClassifierConfig(storedRaw)
			if err != nil {
				return "", fmt.Errorf("%s", safeTypesafeConfigError(storedRaw, err))
			}
			if storedConfig.APIKey == "" {
				return "", fmt.Errorf("api_key masked placeholder cannot be used when no API key is stored")
			}
			restoredKey, err := json.Marshal(storedConfig.APIKey)
			if err != nil {
				return "", err
			}
			fields["api_key"] = restoredKey
		}
	}
	requestConfig, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	config, err := store.ParseTypesafeClassifierConfig(string(requestConfig))
	if err != nil {
		return "", fmt.Errorf("%s", safeTypesafeConfigError(string(requestConfig), err))
	}
	normalized, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	return string(normalized), nil
}

func safeTypesafeConfigError(raw string, err error) string {
	message := err.Error()
	fields := make(map[string]json.RawMessage)
	if json.Unmarshal([]byte(raw), &fields) == nil {
		for _, field := range []string{"api_key", "base_url", "proxy_url"} {
			var value string
			if json.Unmarshal(fields[field], &value) != nil || value == "" {
				continue
			}
			message = strings.ReplaceAll(message, value, "[redacted]")
			trimmed := strings.TrimSpace(value)
			if trimmed != value {
				message = strings.ReplaceAll(message, trimmed, "[redacted]")
			}
			if field == "proxy_url" && !strings.Contains(trimmed, "://") {
				message = strings.ReplaceAll(message, "http://"+trimmed, "[redacted]")
			}
		}
	}
	return message
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.router.Reload()
	writeOK(w, map[string]bool{"ok": true})
}
