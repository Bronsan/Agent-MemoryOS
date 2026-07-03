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

	// Route: exact /admin → redirect to /admin/
	s.mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})

	// Route: /admin/* → SPA + API (Go 1.22 trailing slash matches subtree)
	s.mux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		// API routes take priority
		if strings.HasPrefix(r.URL.Path, "/admin/api/") {
			s.handleAPI(w, r)
			return
		}
		// SPA: serve index.html for all non-API paths
		r.URL.Path = "/index.html"
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

func (s *Server) handleMemories(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"error":    "postgresql_not_connected",
			"message":  "PostgreSQL is not connected. Memory features are unavailable.",
			"memories": []interface{}{},
		})
		return
	}
	limit := 50
	offset := 0
	userID := r.URL.Query().Get("user_id")
	search := r.URL.Query().Get("search")
	level := r.URL.Query().Get("level")

	memories, err := s.memoryStore.ListByUser(r.Context(), userID, limit, offset, storage.SearchFilters{
		Levels: []types.MemoryLevel{types.MemoryLevel(level)},
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	if memories == nil {
		memories = []*types.Memory{}
	}
	// Filter by search if provided
	if search != "" {
		filtered := make([]*types.Memory, 0)
		for _, m := range memories {
			if strings.Contains(strings.ToLower(m.Content), strings.ToLower(search)) {
				filtered = append(filtered, m)
			}
		}
		memories = filtered
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"memories": memories})
	_ = search
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"total_memories": 0,
			"total_entities": 0,
			"by_level":       map[string]int64{},
		})
		return
	}
	stats, err := s.memoryStore.GetStats(r.Context(), "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stats failed"})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handlePluginsList(w http.ResponseWriter, r *http.Request) {
	if s.pluginRegistry == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"plugins": []interface{}{}})
		return
	}
	names := s.pluginRegistry.List()
	type pluginInfo struct {
		Name    string `json:"name"`
		Running bool   `json:"running"`
		Healthy bool   `json:"healthy"`
		Error   string `json:"error,omitempty"`
	}
	list := make([]pluginInfo, 0, len(names))
	for _, name := range names {
		p := pluginInfo{Name: name}
		plug := s.pluginRegistry.Get(name)
		if plug != nil {
			err := plug.Health(r.Context())
			if err != nil {
				p.Error = err.Error()
			} else {
				p.Healthy = true
				p.Running = true
			}
		}
		list = append(list, p)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"plugins": list})
}

func (s *Server) handlePluginAction(w http.ResponseWriter, r *http.Request, path string) {
	if s.pluginRegistry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no plugin registry"})
		return
	}
	// Path: /plugins/{name}/{action}
	parts := strings.Split(strings.TrimPrefix(path, "/plugins/"), "/")
	if len(parts) != 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}
	name, action := parts[0], parts[1]
	var err error
	switch action {
	case "start":
		err = s.pluginRegistry.Start(r.Context(), name)
	case "stop":
		err = s.pluginRegistry.Stop(name)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	// Return sanitized config (strip secrets)
	safe := map[string]interface{}{
		"server": map[string]interface{}{
			"http_port": s.cfg.Server.HTTPPort,
			"tls":       s.cfg.Server.TLS.Enabled,
		},
		"database": map[string]interface{}{
			"host":     s.cfg.Database.Host,
			"port":     s.cfg.Database.Port,
			"dbname":   s.cfg.Database.DBName,
			"ssl_mode": s.cfg.Database.SSLMode,
		},
		"redis": map[string]interface{}{
			"host": s.cfg.Redis.Host,
			"port": s.cfg.Redis.Port,
		},
		"embedding": map[string]interface{}{
			"provider": s.cfg.Embedding.Provider,
			"model":    s.cfg.Embedding.Model,
		},
		"llm": map[string]interface{}{
			"provider": s.cfg.LLM.Provider,
			"model":    s.cfg.LLM.Model,
		},
		"dashboard": map[string]interface{}{
			"port": s.cfg.Dashboard.Port,
		},
		"worker": map[string]interface{}{
			"concurrency": s.cfg.Worker.Concurrency,
		},
	}
	writeJSON(w, http.StatusOK, safe)
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil || s.eventEngine == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":   "postgresql_not_connected",
			"message": "PostgreSQL is required for ingest. Please connect a database and restart.",
		})
		return
	}
	var req struct {
		UserID string `json:"user_id"`
		Source string `json:"source"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	defer r.Body.Close()

	if req.UserID == "" || req.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and text required"})
		return
	}

	memoryID := fmt.Sprintf("%d", time.Now().UnixNano())

	payload := types.RawInputPayload{Text: req.Text, Format: "text"}
	meta := types.EventMeta{
		Source:    req.Source,
		UserID:    req.UserID,
		SessionID: "dashboard",
	}
	if meta.Source == "" {
		meta.Source = "dashboard"
	}

	evt, err := s.eventEngine.Append(r.Context(), memoryID, types.AggregateMemory,
		types.EventRawInput, payload, meta)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist failed"})
		return
	}

	memory := &types.Memory{
		ID:            memoryID,
		UserID:        req.UserID,
		Level:         types.LevelRawEvent,
		Content:       req.Text,
		Importance:    0.5,
		DecayFactor:   1.0,
		SourceEventID: evt.ID,
		Metadata: types.MemoryMeta{
			Source:    meta.Source,
			SessionID: "dashboard",
		},
	}
	if err := s.memoryStore.Create(r.Context(), memory); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create failed"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"event_id":  evt.ID,
		"memory_id": memoryID,
		"status":    "queued",
	})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	w.Write(b)
	_, _ = w.Write([]byte("\n"))
	_ = err
	_ = io.Discard
}
