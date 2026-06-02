// api/server.go
// Web UI 后端 HTTP 路由注册

package api

import (
	"kiro-proxy/router"
	"kiro-proxy/store"
	"net/http"
)

type Server struct {
	db     *store.DB
	router *router.Router
	stats  *store.Stats
	mux    *http.ServeMux
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

// RegisterTo 将 API 路由注册到外部 mux
func (s *Server) RegisterTo(mux *http.ServeMux) {
	s.registerRoutesTo(mux)
}

func (s *Server) registerRoutes() {
	s.registerRoutesTo(s.mux)
}

func (s *Server) registerRoutesTo(mux *http.ServeMux) {
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/models/", s.handleModel)
	mux.HandleFunc("/api/refresh-codex-model", s.handleRefreshCodexModel)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/test-model", s.handleTestModel)
	mux.HandleFunc("/api/reload", s.handleReload)
}
