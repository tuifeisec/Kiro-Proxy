// mode.go
// 代理模式切换：抓包模式 / 转发模式
//
// 抓包模式：拦截记录请求/响应原始数据，然后放行（不做格式转换，不改变流量）
// 转发模式：拦截 → 格式转换 → 转发到用户配置的上游 API

package proxy

import (
	"net/http"
	"sync/atomic"

	json "github.com/goccy/go-json"
)

const (
	ModeForward = "forward" // 转发模式（默认）
	ModeCapture = "capture" // 抓包模式
)

// proxyMode 当前代理模式（0=forward, 1=capture）
var proxyMode atomic.Int32

// GetProxyMode 返回当前模式
func GetProxyMode() string {
	if proxyMode.Load() == 1 {
		return ModeCapture
	}
	return ModeForward
}

// SetProxyMode 设置模式
func SetProxyMode(mode string) {
	if mode == ModeCapture {
		proxyMode.Store(1)
	} else {
		proxyMode.Store(0)
	}
}

// IsCaptureMode 是否抓包模式
func IsCaptureMode() bool {
	return proxyMode.Load() == 1
}

// handleProxyMode GET/PUT /api/proxy-mode
func (s *Server) handleProxyMode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(s.proxyModeStatus())

	case http.MethodPut:
		var body struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
			return
		}
		if body.Mode != ModeForward && body.Mode != ModeCapture {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "mode must be 'forward' or 'capture'"})
			return
		}
		SetProxyMode(body.Mode)
		if s.logger != nil {
			s.logger.Info("proxy", "模式切换: "+body.Mode)
		}
		json.NewEncoder(w).Encode(s.proxyModeStatus())

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
