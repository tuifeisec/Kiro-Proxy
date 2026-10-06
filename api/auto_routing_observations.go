package api

import "net/http"

func (s *Server) handleAutoRoutingObservations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeOK(w, s.router.GetAutoObservations())
}
