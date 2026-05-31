// api/settings.go
// 全局设置读写接口

package api

import "net/http"

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.db.GetAllSettings()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w, settings)

	case http.MethodPut:
		var body map[string]string
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		for k, v := range body {
			if err := s.db.SetSetting(k, v); err != nil {
				writeError(w, http.StatusInternalServerError, "设置 "+k+" 失败: "+err.Error())
				return
			}
		}
		// 如果修改了 active_mapping_id，同步更新路由器
		if id, ok := body["active_model_id"]; ok {
			_ = id // 不再需要 active model 概念
		}
		writeOK(w, map[string]bool{"ok": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.router.Reload()
	writeOK(w, map[string]bool{"ok": true})
}
