package api

import (
	"log"
	"net/http"
	"time"

	"github.com/agent-memoryos/memory-core/auth"
	"github.com/agent-memoryos/memory-core/config"
)

// Router sets up the HTTP router with all security middleware.
func Router(h *Handler, authMiddleware *auth.Middleware, cfg config.ServerConfig) http.Handler {
	mux := http.NewServeMux()

	// Health check (no auth, but rate-limited)
	mux.HandleFunc("GET /health", h.Health)

	// Memory ingestion — fast write path
	mux.HandleFunc("POST /api/v1/ingest", authMiddleware.Wrap(h.IngestMemory))

	// Memory retrieval
	mux.HandleFunc("GET /api/v1/memories/{id}", authMiddleware.Wrap(h.GetMemory))

	// Hybrid search
	mux.HandleFunc("POST /api/v1/search", authMiddleware.Wrap(h.SearchMemory))

	// Stats
	mux.HandleFunc("GET /api/v1/stats", authMiddleware.Wrap(h.GetStats))

	// Apply middleware stack (outermost first applied)
	var handler http.Handler = mux

	// 1. Panic recovery (innermost — catches everything)
	handler = RecoveryMiddleware(handler)

	// 2. Request ID injection
	handler = RequestIDMiddleware(handler)

	// 3. Timeout
	if cfg.WriteTimeout > 0 {
		handler = TimeoutMiddleware(cfg.WriteTimeout)(handler)
	}

	// 4. Security headers
	handler = SecurityHeadersMiddleware(handler)

	// 5. Rate limiting (100 req/s per IP, burst 200 for ingest traffic)
	rateLimiter := NewRateLimiter(100, 200)
	handler = rateLimiter.Middleware(handler)

	// 6. Body size limit
	handler = BodyLimitMiddleware(cfg.MaxBodySize)(handler)

	// 7. CORS (configurable, defaults to restricted)
	handler = corsMiddleware(handler, cfg.EnableCORS)

	// 8. Request logging (outermost)
	handler = loggingMiddleware(handler)

	return handler
}

// loggingMiddleware performs structured request logging (no sensitive data).
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		elapsed := time.Since(start)
		reqID := GetRequestID(r.Context())
		log.Printf("[%s] %s %s %d %s",
			reqID, r.Method, r.URL.Path, wrapped.statusCode, elapsed)
	})
}

// corsMiddleware adds CORS headers with secure defaults.
func corsMiddleware(next http.Handler, enabled bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if enabled {
			// Secure default: reflect the Origin header rather than wildcard
			origin := r.Header.Get("Origin")
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers",
				"Content-Type, Authorization, X-API-Key, X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "3600")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// responseWriter captures the status code for logging.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
