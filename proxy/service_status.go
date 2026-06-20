package proxy

import (
	"fmt"
	"net/http"

	json "github.com/goccy/go-json"
)

type serviceStatusResp struct {
	Running           bool   `json:"running"`
	ProxyRunning      bool   `json:"proxy_running"`
	UIRunning         bool   `json:"ui_running"`
	Host              string `json:"host"`
	ProxyPort         int    `json:"proxy_port"`
	UIPort            int    `json:"ui_port"`
	ProxyURL          string `json:"proxy_url"`
	UIURL             string `json:"ui_url"`
	AccessMode        string `json:"access_mode"`
	LAN               bool   `json:"lan"`
	Protocol          string `json:"protocol"`
	Mode              string `json:"mode"`
	UptimeSeconds     int64  `json:"uptime_seconds"`
	ActiveConnections int    `json:"active_connections"`
}

// handleServiceStatus 返回 Web UI 与 MITM 代理的运行状态。
func (s *Server) handleServiceStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	host := s.proxyDisplayHost()
	accessMode := s.proxyAccessMode()
	proxyPort := s.cfg.Proxy.Port
	uiPort := s.UIPort()
	snapshot := s.stats.Snapshot()
	proxyRunning := s.mitmServer != nil
	uiRunning := true

	json.NewEncoder(w).Encode(serviceStatusResp{
		Running:           proxyRunning && uiRunning,
		ProxyRunning:      proxyRunning,
		UIRunning:         uiRunning,
		Host:              host,
		ProxyPort:         proxyPort,
		UIPort:            uiPort,
		ProxyURL:          fmt.Sprintf("http://%s:%d", host, proxyPort),
		UIURL:             fmt.Sprintf("http://%s:%d", host, uiPort),
		AccessMode:        accessMode,
		LAN:               accessMode == ProxyAccessLAN,
		Protocol:          "HTTP / HTTPS MITM",
		Mode:              GetProxyMode(),
		UptimeSeconds:     snapshot.UptimeSeconds,
		ActiveConnections: s.hub.ClientCount(),
	})
}
