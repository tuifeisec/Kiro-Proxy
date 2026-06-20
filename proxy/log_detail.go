package proxy

import (
	"net/http"

	json "github.com/goccy/go-json"
)

const (
	LogDetailSummary = "summary"
	LogDetailFull    = "full"
)

// handleLogDetail GET/PUT /api/log-detail
// 允许前端日志页即时切换 detail 日志模式：summary 默认摘要，full 完整日志。
func (s *Server) handleLogDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.requireTrafficLogPermission(w) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		opts := s.logger.Options()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"mode":     opts.DetailMode,
			"max_body": opts.MaxDetailBytes,
		})

	case http.MethodPut:
		var body struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
			return
		}
		if body.Mode != LogDetailSummary && body.Mode != LogDetailFull {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "mode must be 'summary' or 'full'"})
			return
		}

		s.cfg.Proxy.LogDetail = body.Mode
		s.logger.SetOptions(LogOptions{DetailMode: body.Mode, MaxDetailBytes: s.cfg.Proxy.LogMaxBody})
		s.logger.Info("proxy", "日志模式切换: "+body.Mode)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"mode":     body.Mode,
			"max_body": s.cfg.Proxy.LogMaxBody,
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
