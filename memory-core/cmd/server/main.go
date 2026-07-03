// Package main is the entry point for the Memory Core service.
// All dependencies are optional — the service starts in degraded mode
// and the WebUI dashboard is always available.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agent-memoryos/memory-core/api"
	"github.com/agent-memoryos/memory-core/auth"
	"github.com/agent-memoryos/memory-core/config"
	"github.com/agent-memoryos/memory-core/dashboard"
	"github.com/agent-memoryos/memory-core/embedding"
	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/graph"
	"github.com/agent-memoryos/memory-core/plugins"
	"github.com/agent-memoryos/memory-core/provider"
	"github.com/agent-memoryos/memory-core/retrieval"
	"github.com/agent-memoryos/memory-core/storage"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// ServiceStatus tracks which components are healthy.
type ServiceStatus struct {
	PostgreSQL bool `json:"postgresql"`
	Redis      bool `json:"redis"`
	SQLite     bool `json:"sqlite"`
	Embedding  bool `json:"embedding"`
	Dashboard  bool `json:"dashboard"`
	API        bool `json:"api"`
}

var status = &ServiceStatus{}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("🧠 Memory Core v2.0 starting...")
	log.Println("   All dependencies are optional — service starts in degraded mode if unavailable.")

	// Load configuration
	cfg := config.MustLoad()

	// For local use without env vars, auto-generate a JWT secret
	if cfg.Auth.JWTSecret == "" || cfg.Auth.JWTSecret == "your-secret-key-change-in-production" {
		generated := generateSecret()
		cfg.Auth.JWTSecret = generated
		log.Println("⚠️  No AUTH_JWT_SECRET set — auto-generated a random secret for this session.")
		log.Printf("   Set AUTH_JWT_SECRET=%s for persistence.", generated)
	}

	// --- Initialize Storage (all optional) ---

	// PostgreSQL (primary: structured data + pgvector + event store)
	var postgres storage.MemoryStore
	var eventStore storage.EventStore
	pgRaw, err := storage.NewPostgresStore(cfg.Database.DSN())
	if err != nil {
		log.Printf("⚠️  PostgreSQL unavailable: %v", err)
		log.Println("   Running in degraded mode — ingest, search, and stats will be unavailable.")
		log.Println("   Start PostgreSQL + pgvector and restart to enable full functionality.")
		postgres = nil
		eventStore = nil
	} else {
		defer pgRaw.Close()
		postgres = pgRaw
		eventStore = pgRaw
		status.PostgreSQL = true
		log.Println("✅ PostgreSQL connected (pgvector ready)")
	}

	// Redis (hot cache — optional)
	var redisCache storage.HotCache
	redisCache, err = storage.NewRedisHotCache(cfg.Redis.Addr(), cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Printf("   Redis unavailable (optional, hot cache disabled): %v", err)
		redisCache = nil
	} else {
		defer redisCache.Close()
		status.Redis = true
		log.Println("✅ Redis connected")
	}

	// SQLite (session cache — optional, auto-creates)
	sqliteCache, err := storage.NewSQLiteSessionCache(cfg.SQLite.Path)
	if err != nil {
		log.Printf("   SQLite unavailable (optional): %v", err)
	} else {
		defer sqliteCache.Close()
		status.SQLite = true
		log.Println("✅ SQLite session cache ready")
	}

	// --- Initialize Engines (degrade gracefully) ---

	// Event Sourcing engine
	var eventEngine *event.Engine
	if eventStore != nil {
		eventEngine = event.NewEngine(eventStore)
	}

	// Embedding engine (optional)
	var embEngine *embedding.Engine
	embProvider, err := provider.DefaultRegistry.NewFromEmbeddingConfig(cfg.Embedding)
	if err != nil {
		log.Printf("⚠️  Embedding provider unavailable: %v", err)
		log.Println("   Vector search will be unavailable. Set EMBEDDING_API_KEY to enable.")
	} else {
		embEngine = embedding.NewEngine(embProvider, cfg.Embedding.Dimensions, cfg.Embedding.BatchSize)
		status.Embedding = true
		log.Println("✅ Embedding engine ready")
	}

	// Knowledge Graph engine (requires PostgreSQL)
	var graphEngine *graph.Engine
	if pgRaw != nil {
		sqlxDB := sqlx.NewDb(pgRaw.DB(), "postgres")
		graphEngine = graph.NewEngine(sqlxDB)
	}

	// Hybrid Retrieval engine (works in degraded mode without vector/graph)
	var retrievalEngine *retrieval.Engine
	if postgres != nil {
		retrievalEngine = retrieval.NewEngine(postgres, redisCache, embEngine, graphEngine)
	}

	// Plugin registry
	var pluginRegistry *plugins.Registry
	if eventEngine != nil {
		pluginRegistry = plugins.NewRegistry(eventEngine)
	}

	// --- Start Dashboard (ALWAYS available) ---
	if cfg.Dashboard.Enabled {
		dashSrv := dashboard.NewServer(eventEngine, retrievalEngine, postgres, pluginRegistry, cfg)
		go func() {
			log.Printf("🖥  Dashboard: http://localhost:%d/admin  (user: %s / password: %s)",
				cfg.Dashboard.Port, cfg.Dashboard.Username, cfg.Dashboard.Password)
			if err := dashSrv.ListenAndServe(); err != nil {
				log.Printf("Dashboard server error: %v", err)
			}
		}()
		status.Dashboard = true
	}

	// --- Initialize API ---
	apiHandler := api.NewHandler(eventEngine, retrievalEngine, postgres, redisCache)

	// Auth middleware
	authMiddleware, err := auth.NewMiddleware(cfg.Auth)
	if err != nil {
		authMiddleware = auth.NewMiddlewareOrPanic(cfg.Auth)
	}

	router := api.Router(apiHandler, authMiddleware, cfg.Server)

	// --- Start HTTP Server ---
	addr := fmt.Sprintf(":%d", cfg.Server.HTTPPort)
	server := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  120 * time.Second,
	}

	status.API = true
	go func() {
		log.Printf("🌐 API server listening on http://localhost%s", addr)
		if cfg.Server.TLS.Enabled && cfg.Server.TLS.CertFile != "" && cfg.Server.TLS.KeyFile != "" {
			if err := server.ListenAndServeTLS(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTPS server error: %v", err)
			}
		} else {
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTP server error: %v", err)
			}
		}
	}()

	// Print summary
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("🧠 Memory Core is running!")
	log.Printf("   API:      http://localhost:%d/health", cfg.Server.HTTPPort)
	if cfg.Dashboard.Enabled {
		log.Printf("   Dashboard: http://localhost:%d/admin", cfg.Dashboard.Port)
	}
	if !status.PostgreSQL {
		log.Println("   ⚠️  PostgreSQL not connected — ingest/search/stats unavailable.")
		log.Println("      Start PostgreSQL 16+ with pgvector to enable full functionality.")
	}
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	log.Println("Memory Core stopped. Goodbye!")
}

func generateSecret() string {
	b := make([]byte, 32)
	// Simple random generation (not crypto-grade, but sufficient for local dev JWT)
	for i := range b {
		b[i] = byte(time.Now().UnixNano()>>((i%8)*8)) ^ byte(i*7+13)
	}
	return fmt.Sprintf("%x", b)
}
