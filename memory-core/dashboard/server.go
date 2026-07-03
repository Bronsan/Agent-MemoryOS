// Package dashboard provides an embeddable admin web UI for managing Memory Core.
// It serves a single-page application at /admin with a JSON API at /admin/api/*.
package dashboard

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/agent-memoryos/memory-core/config"
	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/plugins"
	"github.com/agent-memoryos/memory-core/retrieval"
	"github.com/agent-memoryos/memory-core/storage"
	"github.com/agent-memoryos/memory-core/types"
)

//go:embed static/*
var staticFS embed.FS

// Server is the dashboard HTTP server that serves the admin UI and management API.
type Server struct {
	eventEngine     *event.Engine
	retrievalEngine *retrieval.Engine
	memoryStore     storage.MemoryStore
	pluginRegistry  *plugins.Registry
	cfg             *config.Config

	username string
	password string
	mux      *http.ServeMux
}

// NewServer creates a new dashboard server wired to the existing engines and stores.
func NewServer(
	eventEngine *event.Engine,
	retrievalEngine *retrieval.Engine,
	memoryStore storage.MemoryStore,
	pluginRegistry *plugins.Registry,
	cfg *config.Config,
) *Server {
	s := &Server{
		eventEngine:     eventEngine,
		retrievalEngine: retrievalEngine,
		memoryStore:     memoryStore,
		pluginRegistry:  pluginRegistry,
		cfg:             cfg,
		username:        cfg.Dashboard.Username,
		password:        cfg.Dashboard.Password,
		mux:             http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

// ListenAndServe starts the dashboard HTTP server on the configured port.
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf(":%d", s.cfg.Dashboard.Port)
	hsrv := &http.Server{
		Addr:         addr,
		Handler:      s.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	log.Printf("[dashboard] Admin UI listening on http://localhost%s/admin", addr)
	return hsrv.ListenAndServe()
}

func (s *Server) registerRoutes() {
	// Strip the "static/" prefix from embedded files
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(fmt.Sprintf("dashboard: failed to sub static FS: %v", err))
	}

	fileServer := http.FileServer(http.FS(staticSub))

	s.mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		// Redirect /admin to /admin/ so relative asset paths work
		if r.URL.Path == "/admin" {
			http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
			return
		}
		// Serve the SPA: all /admin/* paths serve index.html except /admin/api/*
		if strings.HasPrefix(r.URL.Path, "/admin/api/") {
			s.handleAPI(w, r)
			return
		}
		// Remove /admin prefix for the file server
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/admin")
		if r.URL.Path == "" || r.URL.Path == "/" {
			r.URL.Path = "/index.html"
		}
		fileServer.ServeHTTP(w, r)
	})
}

// --- API Routing ---

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	// Basic Auth guard
	if !s.checkAuth(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Dashboard"`)
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	path := strings.TrimPrefix(r.URL.Path, "/admin/api")

	switch {
	case path == "/memories" && r.Method == http.MethodGet:
		s.handleMemories(w, r)
	case path == "/stats" && r.Method == http.MethodGet:
		s.handleStats(w, r)
	case path == "/plugins" && r.Method == http.MethodGet:
		s.handlePluginsList(w, r)
	case strings.HasPrefix(path, "/plugins/") && r.Method == http.MethodPost:
		s.handlePluginAction(w, r, path)
	case path == "/config" && r.Method == http.MethodGet:
		s.handleConfig(w, r)
	case path == "/ingest" && r.Method == http.MethodPost:
		s.handleIngest(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// --- Auth ---

func (s *Server) checkAuth(r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	userOk := subtle.ConstantTimeCompare([]byte(user), []byte(s.username)) == 1
	passOk := subtle.ConstantTimeCompare([]byte(pass), []byte(s.password)) == 1
	return userOk && passOk
}

// --- Handlers ---

// GET /admin/api/memories?user_id=&limit=20&offset=0&level=&search=
func (s *Server) handleMemories(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	limit := parseIntParam(r, "limit", 20)
	offset := parseIntParam(r, "offset", 0)
	search := r.URL.Query().Get("search")

	if search != "" {
		// Use retrieval engine for search
		query := &types.SearchQuery{
			UserID: userID,
			Query:  search,
			TopK:   limit,
		}
		results, err := s.retrievalEngine.Search(r.Context(), query)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		memories := make([]types.Memory, 0, len(results))
		for _, res := range results {
			memories = append(memories, res.Memory)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"memories": memories,
			"total":    len(memories),
		})
		return
	}

	filters := storage.SearchFilters{
		UserID: userID,
	}
	if lvl := r.URL.Query().Get("level"); lvl != "" {
		filters.Levels = []types.MemoryLevel{types.MemoryLevel(lvl)}
	}

	memories, err := s.memoryStore.ListByUser(r.Context(), userID, limit, offset, filters)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Fallback: if no user-specific memories, return stub
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"memories": memories,
		"total":    len(memories),
	})
}

// GET /admin/api/stats?user_id=
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")

	stats, err := s.memoryStore.GetStats(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// GET /admin/api/plugins
func (s *Server) handlePluginsList(w http.ResponseWriter, r *http.Request) {
	names := s.pluginRegistry.List()
	type pluginInfo struct {
		Name    string `json:"name"`
		Running bool   `json:"running"`
		Healthy bool   `json:"healthy"`
		Error   string `json:"error,omitempty"`
	}

	plugins := make([]pluginInfo, 0, len(names))
	for _, name := range names {
		p := s.pluginRegistry.Get(name)
		info := pluginInfo{Name: name}
		if p == nil {
			info.Error = "not found"
			plugins = append(plugins, info)
			continue
		}
		if err := p.Health(r.Context()); err != nil {
			info.Error = err.Error()
		} else {
			info.Healthy = true
			info.Running = true
		}
		plugins = append(plugins, info)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"plugins": plugins,
	})
}

// POST /admin/api/plugins/{name}/start or /stop
func (s *Server) handlePluginAction(w http.ResponseWriter, r *http.Request, path string) {
	// Parse: /plugins/{name}/{action}
	parts := strings.Split(strings.TrimPrefix(path, "/plugins/"), "/")
	if len(parts) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}
	name := parts[0]
	action := parts[1]

	switch action {
	case "start":
		if err := s.pluginRegistry.Start(r.Context(), name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "started", "name": name})
	case "stop":
		if err := s.pluginRegistry.Stop(name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "name": name})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + action})
	}
}

// GET /admin/api/config
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	// Return sanitized config (no secrets)
	sanitized := map[string]interface{}{
		"server": map[string]interface{}{
			"http_port":      s.cfg.Server.HTTPPort,
			"grpc_port":      s.cfg.Server.GRPCPort,
			"read_timeout":   s.cfg.Server.ReadTimeout.String(),
			"write_timeout":  s.cfg.Server.WriteTimeout.String(),
			"enable_cors":    s.cfg.Server.EnableCORS,
			"enable_metrics": s.cfg.Server.EnableMetrics,
			"tls_enabled":    s.cfg.Server.TLS.Enabled,
		},
		"database": map[string]interface{}{
			"host":     s.cfg.Database.Host,
			"port":     s.cfg.Database.Port,
			"user":     s.cfg.Database.User,
			"db_name":  s.cfg.Database.DBName,
			"ssl_mode": s.cfg.Database.SSLMode,
			// password omitted
		},
		"redis": map[string]interface{}{
			"host": s.cfg.Redis.Host,
			"port": s.cfg.Redis.Port,
			"db":   s.cfg.Redis.DB,
			// password omitted
		},
		"embedding": map[string]interface{}{
			"provider":   s.cfg.Embedding.Provider,
			"model":      s.cfg.Embedding.Model,
			"dimensions": s.cfg.Embedding.Dimensions,
			// api_key omitted
		},
		"llm": map[string]interface{}{
			"provider":   s.cfg.LLM.Provider,
			"model":      s.cfg.LLM.Model,
			"max_tokens": s.cfg.LLM.MaxTokens,
			// api_key omitted
		},
		"worker": map[string]interface{}{
			"concurrency":  s.cfg.Worker.Concurrency,
			"queue_size":   s.cfg.Worker.QueueSize,
			"task_timeout": s.cfg.Worker.TaskTimeout.String(),
		},
		"dashboard": map[string]interface{}{
			"port": s.cfg.Dashboard.Port,
			// username/password omitted
		},
	}
	writeJSON(w, http.StatusOK, sanitized)
}

// POST /admin/api/ingest
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body: " + err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		UserID string `json:"user_id"`
		Source string `json:"source"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	if req.UserID == "" || req.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and text are required"})
		return
	}
	if req.Source == "" {
		req.Source = "dashboard"
	}

	evt, err := plugins.IngestToEvent(r.Context(), s.eventEngine, req.UserID, "", "", req.Source, req.Text)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "ingested",
		"event_id": evt.ID,
		"user_id":  req.UserID,
		"source":   req.Source,
	})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func parseIntParam(r *http.Request, key string, defaultVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
		return n
	}
	return defaultVal
}
