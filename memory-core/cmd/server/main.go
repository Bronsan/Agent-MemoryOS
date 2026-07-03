// Package main is the entry point for the Memory Core service.
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
	"github.com/agent-memoryos/memory-core/embedding"
	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/graph"
	"github.com/agent-memoryos/memory-core/provider"
	"github.com/agent-memoryos/memory-core/retrieval"
	"github.com/agent-memoryos/memory-core/storage"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Memory Core...")

	// Load configuration
	cfg := config.MustLoad()

	// Validate security-critical configuration
	if cfg.Auth.JWTSecret == "" || cfg.Auth.JWTSecret == "your-secret-key-change-in-production" {
		log.Fatal("FATAL: AUTH_JWT_SECRET is not set or is using the default placeholder. " +
			"Generate a strong secret: openssl rand -base64 64")
	}
	if cfg.Auth.EnableAPIKeyAuth && cfg.Auth.JWTSecret == "" {
		log.Fatal("FATAL: API key auth enabled but no JWT secret set")
	}

	// --- Initialize Storage ---

	// PostgreSQL (primary: structured data + pgvector + event store)
	postgres, err := storage.NewPostgresStore(cfg.Database.DSN())
	if err != nil {
		log.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}
	defer postgres.Close()
	log.Println("PostgreSQL connected")

	// Redis (hot cache — optional)
	var redisCache storage.HotCache
	redisCache, err = storage.NewRedisHotCache(cfg.Redis.Addr(), cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Printf("WARNING: Redis unavailable: %v — running without hot cache", err)
		redisCache = nil // nil-safe in handlers
	} else {
		defer redisCache.Close()
		log.Println("Redis connected")
	}

	// SQLite (session cache — optional)
	sqliteCache, err := storage.NewSQLiteSessionCache(cfg.SQLite.Path)
	if err != nil {
		log.Printf("WARNING: SQLite unavailable: %v — running without session cache", err)
	} else {
		defer sqliteCache.Close()
		log.Println("SQLite session cache ready")
	}

	// --- Initialize Engines ---

	// Event Sourcing engine
	eventEngine := event.NewEngine(postgres)

	// Embedding engine — resolved via provider registry
	embProvider, err := provider.DefaultRegistry.NewFromEmbeddingConfig(cfg.Embedding)
	if err != nil {
		log.Fatalf("Failed to create embedding provider: %v", err)
	}
	embEngine := embedding.NewEngine(embProvider, cfg.Embedding.Dimensions, cfg.Embedding.BatchSize)

	// Knowledge Graph engine (wraps PostgreSQL with sqlx)
	sqlxDB := sqlx.NewDb(postgres.DB(), "postgres")
	graphEngine := graph.NewEngine(sqlxDB)

	// Hybrid Retrieval engine
	retrievalEngine := retrieval.NewEngine(postgres, redisCache, embEngine, graphEngine)

	// --- Wire Async Pipeline ---
	// TODO: Connect scheduler.WorkerPool with event handlers

	// --- Initialize API ---

	apiHandler := api.NewHandler(eventEngine, retrievalEngine, postgres, redisCache)

	// Auth middleware — validates JWT secret at startup
	authMiddleware, err := auth.NewMiddleware(cfg.Auth)
	if err != nil {
		log.Fatalf("FATAL: auth initialization failed: %v", err)
	}

	router := api.Router(apiHandler, authMiddleware, cfg.Server)

	// --- Start HTTP Server (with optional TLS) ---

	addr := fmt.Sprintf(":%d", cfg.Server.HTTPPort)
	server := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  120 * time.Second,
		// Disable HTTP/2 server push (mitigates CVE-2023-44487 style resource exhaustion)
		// MaxHeaderBytes: 1 << 20, // 1MB max header size
	}

	// Graceful shutdown
	go func() {
		if cfg.Server.TLS.Enabled && cfg.Server.TLS.CertFile != "" && cfg.Server.TLS.KeyFile != "" {
			log.Printf("HTTPS server listening on %s", addr)
			if err := server.ListenAndServeTLS(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTPS server error: %v", err)
			}
		} else {
			log.Printf("HTTP server listening on %s (TLS disabled)", addr)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTP server error: %v", err)
			}
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server shutdown error: %v", err)
	}

	log.Println("Memory Core stopped")
}
