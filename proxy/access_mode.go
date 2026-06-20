package proxy

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	json "github.com/goccy/go-json"
)

const (
	ProxyAccessLocal = "local"
	ProxyAccessLAN   = "lan"
	proxyAccessKey   = "proxy_access_mode"
)

func (s *Server) proxyAccessMode() string {
	value, err := s.db.GetSetting(proxyAccessKey)
	if err != nil || value != ProxyAccessLAN {
		return ProxyAccessLocal
	}
	return ProxyAccessLAN
}

func (s *Server) proxyDisplayHost() string {
	if s.proxyAccessMode() != ProxyAccessLAN {
		return "127.0.0.1"
	}
	if ip := firstLANIPv4(); ip != "" {
		return ip
	}
	return "0.0.0.0"
}

func (s *Server) proxyBindHost() string {
	if s.proxyAccessMode() == ProxyAccessLAN {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

func (s *Server) handleProxyAccessMode(w http.ResponseWriter, r *http.Request) {
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
		s.writeProxyAccessMode(w)
	case http.MethodPut:
		var body struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
			return
		}
		mode := strings.TrimSpace(body.Mode)
		if mode != ProxyAccessLocal && mode != ProxyAccessLAN {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "mode must be local or lan"})
			return
		}
		if err := s.db.SetSetting(proxyAccessKey, mode); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		s.writeProxyAccessMode(w)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) writeProxyAccessMode(w http.ResponseWriter) {
	host := s.proxyDisplayHost()
	mode := s.proxyAccessMode()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"mode":      mode,
		"lan":       mode == ProxyAccessLAN,
		"host":      host,
		"proxy_url": "http://" + host + ":" + strconv.Itoa(s.cfg.Proxy.Port),
	})
}

func firstLANIPv4() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip = ip.To4()
			if ip == nil || ip.IsLoopback() {
				continue
			}
			return ip.String()
		}
	}
	return ""
}
