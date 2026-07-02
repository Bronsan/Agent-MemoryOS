// Package api implements the HTTP and gRPC API layer for Memory Core.
package api

import (
	"encoding/json"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/retrieval"
	"github.com/agent-memoryos/memory-core/storage"
	"github.com/agent-memoryos/memory-core/types"
	"github.com/google/uuid"
)

const (
	maxIngestTextBytes = 1 << 20 // 1 MB max per ingest text
	maxTopK            = 100     // maximum search results
	maxPageSize        = 1000    // maximum page size for list operations
)

// Handler holds all HTTP API handlers.
type Handler struct {
	eventEngine     *event.Engine
	retrievalEngine *retrieval.Engine
	memoryStore     storage.MemoryStore
	hotCache        storage.HotCache
}

// NewHandler creates a new API handler.
func NewHandler(
	eventEngine *event.Engine,
	retrievalEngine *retrieval.Engine,
	memoryStore storage.MemoryStore,
	hotCache storage.HotCache,
) *Handler {
	return &Handler{
		eventEngine:     eventEngine,
		retrievalEngine: retrievalEngine,
		memoryStore:     memoryStore,
		hotCache:        hotCache,
	}
}

// --- Request/Response types ---

type IngestRequest struct {
	UserID    string                 `json:"user_id"`
	AgentID   string                 `json:"agent_id,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	Text      string                 `json:"text"`
	Source    string                 `json:"source"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

type IngestResponse struct {
	EventID  string `json:"event_id"`
	MemoryID string `json:"memory_id"`
	Status   string `json:"status"`
}

type SearchRequest struct {
	UserID        string   `json:"user_id"`
	Query         string   `json:"query"`
	Keywords      []string `json:"keywords,omitempty"`
	Entities      []string `json:"entities,omitempty"`
	Levels        []string `json:"levels,omitempty"`
	TopK          int      `json:"top_k"`
	MinImportance float64  `json:"min_importance,omitempty"`
}

type SearchResponse struct {
	Results []types.SearchResult `json:"results"`
	TookMs  int64                `json:"took_ms"`
}

type MemoryResponse struct {
	Memory *types.Memory `json:"memory"`
}

type StatsResponse struct {
	Stats *types.MemoryStats `json:"stats"`
}

// --- Handlers ---

// IngestMemory handles the fast write path.
func (h *Handler) IngestMemory(w http.ResponseWriter, r *http.Request) {
	var req IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	defer r.Body.Close()

	// Validate required fields
	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text field is required")
		return
	}
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "source field is required")
		return
	}

	// Validate text size
	if len(req.Text) > maxIngestTextBytes {
		writeError(w, http.StatusBadRequest, "text exceeds maximum size of 1MB")
		return
	}
	if !utf8.ValidString(req.Text) {
		writeError(w, http.StatusBadRequest, "text must be valid UTF-8")
		return
	}

	// Validate UserID length
	if len(req.UserID) > 256 {
		writeError(w, http.StatusBadRequest, "user_id exceeds maximum length")
		return
	}

	// Sanitize: trim source to reasonable length
	if len(req.Source) > 64 {
		req.Source = req.Source[:64]
	}

	ctx := r.Context()

	// 1. Persist as a raw event (fast path)
	memoryID := uuid.New().String()
	payload := types.RawInputPayload{
		Text:   req.Text,
		Format: "text",
	}

	meta := types.EventMeta{
		Source:        req.Source,
		UserID:        req.UserID,
		AgentID:       req.AgentID,
		SessionID:     req.SessionID,
		CorrelationID: uuid.New().String(),
	}

	evt, err := h.eventEngine.Append(ctx, memoryID, types.AggregateMemory,
		types.EventRawInput, payload, meta)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist event")
		return
	}

	// 2. Create initial memory record
	memory := &types.Memory{
		ID:            memoryID,
		UserID:        req.UserID,
		AgentID:       req.AgentID,
		Level:         types.LevelRawEvent,
		Content:       req.Text,
		Importance:    0.5,
		DecayFactor:   1.0,
		SourceEventID: evt.ID,
		Metadata: types.MemoryMeta{
			Source:    req.Source,
			SessionID: req.SessionID,
		},
	}

	if err := h.memoryStore.Create(ctx, memory); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create memory")
		return
	}

	// 3. Cache in hot cache
	if h.hotCache != nil {
		_ = h.hotCache.SetMemory(ctx, memory, 3600)
	}

	writeJSON(w, http.StatusCreated, IngestResponse{
		EventID:  evt.ID,
		MemoryID: memoryID,
		Status:   "queued",
	})
}

// SearchMemory performs hybrid search across all memory signals.
func (h *Handler) SearchMemory(w http.ResponseWriter, r *http.Request) {
	var req SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	defer r.Body.Close()

	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	// Cap TopK to prevent resource exhaustion
	if req.TopK <= 0 {
		req.TopK = 10
	}
	if req.TopK > maxTopK {
		req.TopK = maxTopK
	}

	// Validate query length
	if len(req.Query) > 4096 {
		writeError(w, http.StatusBadRequest, "query exceeds maximum length")
		return
	}

	start := time.Now()
	ctx := r.Context()

	levels := make([]types.MemoryLevel, len(req.Levels))
	for i, l := range req.Levels {
		levels[i] = types.MemoryLevel(l)
	}

	query := &types.SearchQuery{
		UserID:        req.UserID,
		Query:         req.Query,
		Keywords:      req.Keywords,
		Entities:      req.Entities,
		Levels:        levels,
		TopK:          req.TopK,
		MinImportance: req.MinImportance,
	}

	results, err := h.retrievalEngine.Search(ctx, query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	writeJSON(w, http.StatusOK, SearchResponse{
		Results: results,
		TookMs:  time.Since(start).Milliseconds(),
	})
}

// GetMemory retrieves a single memory by ID (hot cache → DB).
func (h *Handler) GetMemory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id is required")
		return
	}
	// Validate ID format: must be a valid UUID
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid memory id format")
		return
	}

	ctx := r.Context()

	// Try hot cache first
	if h.hotCache != nil {
		mem, err := h.hotCache.GetMemory(ctx, id)
		if err == nil && mem != nil {
			writeJSON(w, http.StatusOK, MemoryResponse{Memory: mem})
			return
		}
	}

	// Fall back to DB
	mem, err := h.memoryStore.GetByID(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}

	// Populate hot cache
	if h.hotCache != nil {
		_ = h.hotCache.SetMemory(ctx, mem, 3600)
	}

	writeJSON(w, http.StatusOK, MemoryResponse{Memory: mem})
}

// GetStats returns memory statistics for a user.
func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "user_id query param is required")
		return
	}
	if len(userID) > 256 {
		writeError(w, http.StatusBadRequest, "user_id exceeds maximum length")
		return
	}

	ctx := r.Context()
	stats, err := h.memoryStore.GetStats(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get stats")
		return
	}

	writeJSON(w, http.StatusOK, StatsResponse{Stats: stats})
}

// Health returns service health status.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Do NOT leak internal errors; only return user-safe messages
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	// Never expose internal error details to clients
	writeJSON(w, status, map[string]interface{}{
		"error":   "request_failed",
		"code":    status,
		"message": message,
	})
}
