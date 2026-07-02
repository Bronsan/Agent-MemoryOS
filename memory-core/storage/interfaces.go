// Package storage defines the storage layer interfaces and implementations.
// Architecture: Redis (Hot) → SQLite (Session Cache) → PostgreSQL (Structured + pgvector) → Object Storage (Raw Payload)
package storage

import (
	"context"

	"github.com/agent-memoryos/memory-core/types"
)

// MemoryStore is the primary interface for memory CRUD operations on PostgreSQL.
type MemoryStore interface {
	// Create persists a new memory and returns its ID.
	Create(ctx context.Context, memory *types.Memory) error

	// GetByID retrieves a single memory by ID.
	GetByID(ctx context.Context, id string) (*types.Memory, error)

	// Update modifies an existing memory.
	Update(ctx context.Context, memory *types.Memory) error

	// Delete removes a memory (soft delete).
	Delete(ctx context.Context, id string) error

	// SearchByVector performs a pure vector similarity search via pgvector.
	SearchByVector(ctx context.Context, embedding []float32, topK int, filters SearchFilters) ([]types.SearchResult, error)

	// SearchByKeyword performs full-text search via PostgreSQL tsvector.
	SearchByKeyword(ctx context.Context, query string, topK int, filters SearchFilters) ([]types.SearchResult, error)

	// ListByUser retrieves memories for a user with pagination.
	ListByUser(ctx context.Context, userID string, limit, offset int, filters SearchFilters) ([]*types.Memory, error)

	// GetStats returns aggregated memory statistics for a user.
	GetStats(ctx context.Context, userID string) (*types.MemoryStats, error)
}

// EventStore is the append-only event store (Event Sourcing).
type EventStore interface {
	// Append writes one or more events atomically to the event stream.
	Append(ctx context.Context, events ...*types.Event) error

	// Load retrieves all events for a given aggregate, ordered by version.
	Load(ctx context.Context, aggregateID string, aggregateType types.AggregateType, fromVersion int64) ([]*types.Event, error)

	// GetLatestVersion returns the most recent version number for an aggregate.
	GetLatestVersion(ctx context.Context, aggregateID string, aggregateType types.AggregateType) (int64, error)

	// Subscribe returns a channel that receives new events matching the filter.
	Subscribe(ctx context.Context, filter EventFilter) (<-chan *types.Event, error)
}

// HotCache defines the Redis hot-cache operations for sub-millisecond access.
type HotCache interface {
	// Close releases resources held by the cache.
	Close() error

	// GetMemory retrieves a memory from the hot cache.
	GetMemory(ctx context.Context, memoryID string) (*types.Memory, error)

	// SetMemory stores a memory in the hot cache with TTL.
	SetMemory(ctx context.Context, memory *types.Memory, ttlSeconds int) error

	// Invalidate removes a memory from the hot cache.
	Invalidate(ctx context.Context, memoryID string) error

	// CacheEmbedding stores a pre-computed embedding in Redis.
	CacheEmbedding(ctx context.Context, key string, embedding []float32) error

	// GetEmbedding retrieves a cached embedding.
	GetEmbedding(ctx context.Context, key string) ([]float32, error)

	// AddToRecent adds a memory ID to the user's recent-access sorted set.
	AddToRecent(ctx context.Context, userID, memoryID string, score float64) error

	// GetRecent retrieves the N most recently accessed memory IDs for a user.
	GetRecent(ctx context.Context, userID string, limit int) ([]string, error)

	// SetSession stores session-scoped data with TTL.
	SetSession(ctx context.Context, sessionID, key string, value interface{}, ttlSeconds int) error

	// GetSession retrieves session-scoped data.
	GetSession(ctx context.Context, sessionID, key string, target interface{}) error
}

// SessionCache defines SQLite-based session-scoped caching.
type SessionCache interface {
	// StoreSession persists session context for fast reload.
	StoreSession(ctx context.Context, sessionID string, data *SessionData) error

	// LoadSession retrieves a cached session.
	LoadSession(ctx context.Context, sessionID string) (*SessionData, error)

	// DeleteSession removes a session cache entry.
	DeleteSession(ctx context.Context, sessionID string) error

	// StoreConversation appends a turn to the conversation history.
	StoreConversation(ctx context.Context, sessionID string, turn *ConversationTurn) error

	// LoadConversation retrieves the full conversation history for a session.
	LoadConversation(ctx context.Context, sessionID string, limit int) ([]*ConversationTurn, error)
}

// ObjectStore defines blob/object storage for raw payloads.
type ObjectStore interface {
	// Put stores raw data and returns a reference key.
	Put(ctx context.Context, key string, data []byte, contentType string) error

	// Get retrieves raw data by key.
	Get(ctx context.Context, key string) ([]byte, error)

	// Delete removes a stored object.
	Delete(ctx context.Context, key string) error

	// GetURL returns a pre-signed access URL for the object.
	GetURL(ctx context.Context, key string, expirySeconds int) (string, error)
}

// SearchFilters provides common filtering options for queries.
type SearchFilters struct {
	UserID        string              `json:"user_id"`
	AgentID       string              `json:"agent_id,omitempty"`
	Levels        []types.MemoryLevel `json:"levels,omitempty"`
	MinImportance float64             `json:"min_importance,omitempty"`
	TimeRange     types.TimeRange     `json:"time_range,omitempty"`
	Tags          []string            `json:"tags,omitempty"`
	Source        string              `json:"source,omitempty"`
}

// EventFilter is used to subscribe to specific event types.
type EventFilter struct {
	AggregateTypes []types.AggregateType `json:"aggregate_types,omitempty"`
	EventTypes     []types.EventType     `json:"event_types,omitempty"`
	UserID         string                `json:"user_id,omitempty"`
}

// SessionData represents cached session context.
type SessionData struct {
	SessionID    string         `json:"session_id"`
	UserID       string         `json:"user_id"`
	AgentID      string         `json:"agent_id,omitempty"`
	Metadata     map[string]any `json:"metadata"`
	CreatedAt    int64          `json:"created_at"` // unix timestamp
	LastActiveAt int64          `json:"last_active_at"`
}

// ConversationTurn is a single turn in a conversation.
type ConversationTurn struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"` // "user", "assistant", "system"
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
}
