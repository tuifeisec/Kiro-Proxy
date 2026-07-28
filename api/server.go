// api/server.go
// Web UI 后端 HTTP 路由注册

package api

import (
	"kiro-proxy/router"
	"kiro-proxy/store"
	"net/http"
)

type Server struct {
	db             *store.DB
	router         *router.Router
	stats          *store.Stats
	mux            *http.ServeMux
	healthProvider ModelHealthProvider
}

type ModelHealthProvider interface {
	ModelHealthStatuses() []store.ModelHealthStatus
}

func New(db *store.DB, r *router.Router, stats *store.Stats) *Server {
	s := &Server{
		db:     db,
		router: r,
		stats:  stats,
		mux:    http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) SetModelHealthProvider(provider ModelHealthProvider) {
	s.healthProvider = provider
}

// RegisterTo 将 API 路由注册到外部 mux
func (s *Server) RegisterTo(mux *http.ServeMux) {
	s.registerRoutesTo(mux)
}

func (s *Server) registerRoutes() {
	s.registerRoutesTo(s.mux)
}

func (s *Server) registerRoutesTo(mux *http.ServeMux) {
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/request-usage", s.handleRequestUsage)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/models/", s.handleModel)
	mux.HandleFunc("/api/model-groups", s.handleModelGroups)
	mux.HandleFunc("/api/model-groups/status", s.handleModelGroupStatus)
	mux.HandleFunc("/api/model-groups/", s.handleModelGroup)
	mux.HandleFunc("/api/refresh-codex-model", s.handleRefreshCodexModel)
	mux.HandleFunc("/api/codex-oauth/start", s.handleStartCodexOAuth)
	mux.HandleFunc("/api/codex-oauth/exchange", s.handleExchangeCodexOAuth)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/test-model", s.handleTestModel)
	mux.HandleFunc("/api/reload", s.handleReload)
	mux.HandleFunc("/api/license-info", s.handleLicenseInfo)
	mux.HandleFunc("/api/advertisements", s.handleAdvertisements)
	mux.HandleFunc("/api/app-version", s.handleAppVersion)
	mux.HandleFunc("/api/update-info", s.handleUpdateInfo)
	mux.HandleFunc("/api/open-external-url", s.handleOpenExternalURL)
}
