package api

import (
	"kiro-proxy/buildinfo"
	"net/http"
)

func (s *Server) handleAppVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeOK(w, map[string]interface{}{"version": buildinfo.AppVersion, "build": buildinfo.Current()})
}
