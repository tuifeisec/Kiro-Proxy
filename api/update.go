package api

import (
	"errors"
	"kiro-proxy/license"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

func (s *Server) handleUpdateInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	latest, err := license.NewClient(os.Getenv("KIRO_LICENSE_SERVER")).LatestRelease()
	if err != nil {
		writeError(w, http.StatusBadGateway, "获取版本信息失败: "+err.Error())
		return
	}
	writeOK(w, map[string]interface{}{
		"current_version": license.AppVersion,
		"latest_version":  latest.Version,
		"platform":        latest.Platform,
		"has_update":      isNewerVersion(latest.Version, license.AppVersion),
		"download_url":    latest.DownloadURL,
		"notes":           latest.Notes,
	})
}

func (s *Server) handleOpenExternalURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := openExternalURL(body.URL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

func isNewerVersion(latest, current string) bool {
	latestParts, ok := parseVersion(latest)
	if !ok {
		return false
	}
	currentParts, ok := parseVersion(current)
	if !ok {
		return false
	}
	for i := 0; i < len(latestParts); i++ {
		if latestParts[i] > currentParts[i] {
			return true
		}
		if latestParts[i] < currentParts[i] {
			return false
		}
	}
	return false
}

func parseVersion(version string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(version, "v")), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		if part == "" {
			return out, false
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return out, false
		}
		out[i] = value
	}
	return out, true
}

func openExternalURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("无效的下载地址")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("下载地址只支持 http 或 https")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u.String())
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u.String())
	case "linux":
		cmd = exec.Command("xdg-open", u.String())
	default:
		return errors.New("当前系统不支持打开下载地址")
	}
	return cmd.Start()
}
