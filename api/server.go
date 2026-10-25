// api/server.go
// Web UI 后端 HTTP 路由注册

package api

import (
	"kiro-proxy/codex"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"net/http"
)

type Server struct {
	db             *store.DB
	router         *router.Router
	stats          *store.Stats
	credentials    *codex.CredentialManager
	mux            *http.ServeMux
	healthProvider ModelHealthProvider
}

type ModelHealthProvider interface {
	ModelHealthStatuses() []store.ModelHealthStatus
}

func New(db *store.DB, r *router.Router, stats *store.Stats, managers ...*codex.CredentialManager) *Server {
	var credentials *codex.CredentialManager
	if len(managers) > 0 {
		credentials = managers[0]
	}
	if credentials == nil {
		credentials = codex.NewCredentialManager(db)
	}
	s := &Server{
		db:          db,
		router:      r,
		stats:       stats,
		credentials: credentials,
		mux:         http.NewServeMux(),
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
	mux.HandleFunc("/api/upstream-models", s.handleUpstreamModels)
	mux.HandleFunc("/api/model-groups", s.handleModelGroups)
	mux.HandleFunc("/api/model-groups/status", s.handleModelGroupStatus)
	mux.HandleFunc("/api/model-groups/", s.handleModelGroup)
	mux.HandleFunc("/api/refresh-codex-model", s.handleRefreshCodexModel)
	mux.HandleFunc("/api/codex-oauth/start", s.handleStartCodexOAuth)
	mux.HandleFunc("/api/codex-oauth/exchange", s.handleExchangeCodexOAuth)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/auto-routing/observations", s.handleAutoRoutingObservations)
	mux.HandleFunc("/api/test-model", s.handleTestModel)
	mux.HandleFunc("/api/reload", s.handleReload)
	mux.HandleFunc("/api/app-version", s.handleAppVersion)
}
