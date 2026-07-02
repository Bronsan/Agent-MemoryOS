package types

import (
	"encoding/json"
	"time"
)

// EventType enumerates all event types in the Event Sourcing system.
type EventType string

const (
	// Input events (external sources)
	EventRawInput EventType = "raw_input"

	// Processing events (async pipeline)
	EventEntityExtracted    EventType = "entity_extracted"
	EventEmbeddingGenerated EventType = "embedding_generated"
	EventSummaryGenerated   EventType = "summary_generated"
	EventGraphUpdated       EventType = "graph_updated"
	EventImportanceScored   EventType = "importance_scored"
	EventDecayApplied       EventType = "decay_applied"

	// Memory lifecycle events
	EventMemoryCreated  EventType = "memory_created"
	EventMemoryPromoted EventType = "memory_promoted" // level upgrade
	EventMemoryDemoted  EventType = "memory_demoted"
	EventMemoryArchived EventType = "memory_archived"
	EventMemoryAccessed EventType = "memory_accessed"
	EventMemoryMerged   EventType = "memory_merged"

	// Conflict & consistency
	EventConflictDetected EventType = "conflict_detected"
	EventConflictResolved EventType = "conflict_resolved"

	// System events
	EventSnapshotCreated EventType = "snapshot_created"
)

// Event is the immutable record in the Event Sourcing system.
// Every state change is captured as an Event; all derived data can be rebuilt from Events.
type Event struct {
	ID            string          `json:"id"`
	AggregateID   string          `json:"aggregate_id"` // e.g. user_id or memory_id
	AggregateType AggregateType   `json:"aggregate_type"`
	Type          EventType       `json:"type"`
	Version       int64           `json:"version"` // monotonically increasing per aggregate
	Payload       json.RawMessage `json:"payload"` // event-specific data
	Metadata      EventMeta       `json:"metadata"`
	Timestamp     time.Time       `json:"timestamp"`
}

// EventMeta holds contextual information about the event origin.
type EventMeta struct {
	Source        string `json:"source"`         // plugin name or system component
	CorrelationID string `json:"correlation_id"` // trace ID across async pipeline
	CausationID   string `json:"causation_id"`   // parent event ID
	UserID        string `json:"user_id"`
	AgentID       string `json:"agent_id"`
	SessionID     string `json:"session_id"`
}

// AggregateType classifies the aggregate root for event sourcing.
type AggregateType string

const (
	AggregateMemory  AggregateType = "memory"
	AggregateUser    AggregateType = "user"
	AggregateSession AggregateType = "session"
	AggregateGraph   AggregateType = "graph"
)

// EventPayload is the marker interface for event-specific payloads.
type EventPayload interface {
	EventType() EventType
}

// --- Concrete Event Payloads ---

// RawInputPayload carries the raw text/conversation input.
type RawInputPayload struct {
	Text        string       `json:"text"`
	Format      string       `json:"format"` // "text", "markdown", "json"
	Attachments []Attachment `json:"attachments,omitempty"`
}

func (p RawInputPayload) EventType() EventType { return EventRawInput }

// MemoryCreatedPayload is emitted when a new memory is persisted.
type MemoryCreatedPayload struct {
	MemoryID string      `json:"memory_id"`
	Level    MemoryLevel `json:"level"`
	Content  string      `json:"content"`
}

func (p MemoryCreatedPayload) EventType() EventType { return EventMemoryCreated }

// MemoryPromotedPayload is emitted when a memory advances to a higher level.
type MemoryPromotedPayload struct {
	MemoryID    string      `json:"memory_id"`
	FromLevel   MemoryLevel `json:"from_level"`
	ToLevel     MemoryLevel `json:"to_level"`
	NewMemoryID string      `json:"new_memory_id"`
}

func (p MemoryPromotedPayload) EventType() EventType { return EventMemoryPromoted }

// EntityExtractedPayload carries entity extraction results.
type EntityExtractedPayload struct {
	MemoryID string   `json:"memory_id"`
	Entities []Entity `json:"entities"`
}

func (p EntityExtractedPayload) EventType() EventType { return EventEntityExtracted }

// EmbeddingGeneratedPayload carries the generated embedding vector.
type EmbeddingGeneratedPayload struct {
	MemoryID  string    `json:"memory_id"`
	Embedding []float32 `json:"embedding"`
	Model     string    `json:"model"`
}

func (p EmbeddingGeneratedPayload) EventType() EventType { return EventEmbeddingGenerated }

// Attachment represents a file attached to a raw input event.
type Attachment struct {
	ID       string `json:"id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
	Content  string `json:"content,omitempty"` // extracted text
}
