package transport

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"kiro-proxy/go/internal/app"
	"kiro-proxy/go/internal/core"
)

const sessionCookie = "kp_admin_session"

type Server struct {
	service *app.Service
	adminFS fs.FS
}

func NewServer(service *app.Service, adminFS fs.FS) *Server {
	return &Server{service: service, adminFS: adminFS}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/admin/login", s.handleLogin)
	mux.HandleFunc("/api/admin/logout", s.requireAdmin(s.handleLogout))
	mux.HandleFunc("/api/admin/state", s.requireAdmin(s.handleState))
	mux.HandleFunc("/api/admin/dashboard", s.requireAdmin(s.handleDashboard))
	mux.HandleFunc("/api/admin/authorize", s.requireAdmin(s.handleAuthorize))
	mux.HandleFunc("/api/admin/authorizations", s.requireAdmin(s.handleAuthorizations))
	mux.HandleFunc("/api/admin/releases", s.requireAdmin(s.handleReleases))
	mux.HandleFunc("/api/admin/ads", s.requireAdmin(s.handleAds))
	mux.HandleFunc("/api/admin/branding", s.requireAdmin(s.handleBranding))
	mux.HandleFunc("/api/license/check", s.handleLicenseCheck)
	mux.HandleFunc("/api/ads", s.handlePublicAds)
	mux.HandleFunc("/api/releases/latest", s.handleLatestRelease)
	mux.Handle("/sjksjk/", s.adminHandler())
	mux.Handle("/sjksjk", s.adminHandler())
	return mux
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	token, err := s.service.Login(body.Username, body.Password)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, app.ErrInvalidCredential) {
			status = http.StatusUnauthorized
		}
		writeError(w, status, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(12 * time.Hour)})
	writeOK(w, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.service.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeOK(w, map[string]bool{"ok": true})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeOK(w, s.service.Snapshot())
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeOK(w, s.service.DashboardStats())
}

func (s *Server) handleAuthorizations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := s.service.DeleteAuthorization(r.URL.Query().Get("machine_code")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		MachineCode string `json:"machine_code"`
		Remark      string `json:"remark"`
		Enabled     *bool  `json:"enabled"`
		TrafficLogs bool   `json:"traffic_logs"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	auth, err := s.service.Authorize(body.MachineCode, body.Remark, enabled, body.TrafficLogs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w, auth)
}

func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body core.Release
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		item, err := s.service.AddRelease(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeOK(w, item)
	case http.MethodDelete:
		if err := s.service.DeleteRelease(r.URL.Query().Get("platform"), r.URL.Query().Get("version")); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w, map[string]bool{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleAds(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body core.Advertisement
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		item, err := s.service.SaveAdvertisement(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeOK(w, item)
	case http.MethodDelete:
		if err := s.service.DeleteAdvertisement(r.URL.Query().Get("id")); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w, map[string]bool{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body core.Branding
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	branding, err := s.service.SaveBranding(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, branding)
}

func (s *Server) handleLicenseCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body core.LicenseCheckRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	writeOK(w, s.service.CheckLicense(body))
}

func (s *Server) handlePublicAds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeOK(w, s.service.EnabledAdvertisements())
}

func (s *Server) handleLatestRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeOK(w, s.service.LatestRelease(r.URL.Query().Get("platform")))
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || !s.service.ValidSession(cookie.Value) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) adminHandler() http.Handler {
	sub, err := fs.Sub(s.adminFS, "admin/dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusInternalServerError, err.Error())
		})
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/sjksjk")
		if path == "" || path == "/" {
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(sub, strings.TrimPrefix(path, "/")); err != nil {
			http.NotFound(w, r)
			return
		}
		r.URL.Path = path
		fileServer.ServeHTTP(w, r)
	})
}

func readJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func writeOK(w http.ResponseWriter, v interface{}) {
	writeJSON(w, http.StatusOK, v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
