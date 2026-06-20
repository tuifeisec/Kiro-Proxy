package proxy

import "net/http"

func (s *Server) hasTrafficLogPermission() bool {
	result, err := s.licenseClient.CheckCached()
	if err != nil {
		return false
	}
	return result.Authorized && result.TrafficLogs
}

func (s *Server) requireTrafficLogPermission(w http.ResponseWriter) bool {
	if s.hasTrafficLogPermission() {
		return true
	}
	http.NotFound(w, nil)
	return false
}
