// api/models.go
// 模型配置增删改查接口

package api

import (
	"kiro-proxy/store"
	"net/http"
	"strings"
)

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		models, err := s.db.GetModels()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 脱敏 API Key
		for i := range models {
			models[i].APIKey = maskKey(models[i].APIKey)
		}
		writeOK(w, models)

	case http.MethodPost:
		var m store.Model
		if err := readJSON(r, &m); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if m.ModelID == "" || m.BaseURL == "" || m.APIKey == "" || m.Endpoint == "" {
			writeError(w, http.StatusBadRequest, "model_id, base_url, api_key, endpoint 不能为空")
			return
		}
		if m.Provider == "" {
			m.Provider = "openai"
		}
		// 校验 name 不可重复
		if m.Name != "" {
			if dup, _ := s.db.FindModelByName(m.Name); dup != nil {
				writeError(w, http.StatusConflict, "模型名称 \""+m.Name+"\" 已存在，请使用不同的名称")
				return
			}
		}
		m.Enabled = true
		if err := s.db.CreateModel(&m); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.router.Reload()
		m.APIKey = maskKey(m.APIKey)
		writeJSON(w, http.StatusCreated, m)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "/api/models/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing model id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		m, err := s.db.GetModel(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "model not found")
			return
		}
		m.APIKey = maskKey(m.APIKey)
		writeOK(w, m)

	case http.MethodPut:
		var m store.Model
		if err := readJSON(r, &m); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		m.ID = id
		// 校验 name 不可重复（排除自身）
		if m.Name != "" {
			if dup, _ := s.db.FindModelByName(m.Name); dup != nil && dup.ID != id {
				writeError(w, http.StatusConflict, "模型名称 \""+m.Name+"\" 已存在，请使用不同的名称")
				return
			}
		}
		// 如果 API Key 是脱敏值，保留原来的
		if isMasked(m.APIKey) {
			existing, err := s.db.GetModel(id)
			if err == nil {
				m.APIKey = existing.APIKey
			}
		}
		if err := s.db.UpdateModel(&m); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.router.Reload()
		m.APIKey = maskKey(m.APIKey)
		writeOK(w, m)

	case http.MethodDelete:
		if err := s.db.DeleteModel(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.router.Reload()
		writeOK(w, map[string]bool{"ok": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

func isMasked(key string) bool {
	return strings.Contains(key, "****")
}
