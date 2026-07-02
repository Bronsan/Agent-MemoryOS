// Package types defines the core domain types for the Agent Memory OS.
// Memory follows a lifecycle: RawEvent → Episode → Fact → Preference → Personality → Knowledge → Archive
package types

import (
	"time"
)

// MemoryLevel represents the lifecycle stage of a memory.
type MemoryLevel string

const (
	LevelRawEvent    MemoryLevel = "raw_event"
	LevelEpisode     MemoryLevel = "episode"
	LevelFact        MemoryLevel = "fact"
	LevelPreference  MemoryLevel = "preference"
	LevelPersonality MemoryLevel = "personality"
	LevelKnowledge   MemoryLevel = "knowledge"
	LevelArchive     MemoryLevel = "archive"
)

// MemoryLevelOrder maps each level to its position in the lifecycle (higher = more distilled).
var MemoryLevelOrder = map[MemoryLevel]int{
	LevelRawEvent:    0,
	LevelEpisode:     1,
	LevelFact:        2,
	LevelPreference:  3,
	LevelPersonality: 4,
	LevelKnowledge:   5,
	LevelArchive:     6,
}

// Memory represents a single memory record at any lifecycle level.
type Memory struct {
	ID             string      `json:"id"`
	UserID         string      `json:"user_id"`
	AgentID        string      `json:"agent_id"`
	Level          MemoryLevel `json:"level"`
	Content        string      `json:"content"`
	Embedding      []float32   `json:"embedding,omitempty"`
	Entities       []Entity    `json:"entities,omitempty"`
	Relations      []Relation  `json:"relations,omitempty"`
	Importance     float64     `json:"importance"`   // 0.0 - 1.0
	DecayFactor    float64     `json:"decay_factor"` // decay rate for memory strength
	AccessCount    int64       `json:"access_count"`
	LastAccessedAt time.Time   `json:"last_accessed_at"`
	SourceEventID  string      `json:"source_event_id"` // Event Sourcing: root event
	ParentID       string      `json:"parent_id"`       // derived from this memory
	Metadata       MemoryMeta  `json:"metadata"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	ExpiresAt      *time.Time  `json:"expires_at,omitempty"`
}

// MemoryMeta holds contextual metadata for a memory.
type MemoryMeta struct {
	Source     string         `json:"source"` // e.g. "discord", "telegram", "web"
	ChannelID  string         `json:"channel_id"`
	SessionID  string         `json:"session_id"`
	Language   string         `json:"language"`
	Sentiment  float64        `json:"sentiment"` // -1.0 to 1.0
	Tags       []string       `json:"tags"`
	Custom     map[string]any `json:"custom"`
	RawPayload string         `json:"raw_payload"` // reference to object storage
}

// Entity represents a named entity extracted from a memory.
type Entity struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       EntityType     `json:"type"`
	Properties map[string]any `json:"properties"`
	Confidence float64        `json:"confidence"`
}

// EntityType classifies the kind of entity.
type EntityType string

const (
	EntityPerson       EntityType = "person"
	EntityOrganization EntityType = "organization"
	EntityLocation     EntityType = "location"
	EntityDate         EntityType = "date"
	EntityProduct      EntityType = "product"
	EntityEvent        EntityType = "event"
	EntityConcept      EntityType = "concept"
	EntityCustom       EntityType = "custom"
)

// Relation represents a relationship between two entities.
type Relation struct {
	ID         string  `json:"id"`
	SubjectID  string  `json:"subject_id"`
	Predicate  string  `json:"predicate"`
	ObjectID   string  `json:"object_id"`
	Weight     float64 `json:"weight"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"` // event ID that produced this relation
}

// SearchQuery represents a hybrid search request.
type SearchQuery struct {
	UserID        string        `json:"user_id"`
	Query         string        `json:"query"`
	Embedding     []float32     `json:"embedding,omitempty"`
	Keywords      []string      `json:"keywords,omitempty"`
	Entities      []string      `json:"entities,omitempty"`
	TimeRange     TimeRange     `json:"time_range,omitempty"`
	Levels        []MemoryLevel `json:"levels,omitempty"`
	MinImportance float64       `json:"min_importance"`
	TopK          int           `json:"top_k"`
	VectorWeight  float64       `json:"vector_weight"`  // 0.0-1.0
	KeywordWeight float64       `json:"keyword_weight"` // 0.0-1.0
	GraphWeight   float64       `json:"graph_weight"`   // 0.0-1.0
	TimeWeight    float64       `json:"time_weight"`    // 0.0-1.0
}

// TimeRange defines a temporal filter.
type TimeRange struct {
	Start *time.Time `json:"start,omitempty"`
	End   *time.Time `json:"end,omitempty"`
}

// SearchResult holds a single retrieved memory with its relevance score.
type SearchResult struct {
	Memory         Memory         `json:"memory"`
	Score          float64        `json:"score"`
	ScoreBreakdown ScoreBreakdown `json:"score_breakdown"`
}

// ScoreBreakdown shows the contribution of each retrieval strategy.
type ScoreBreakdown struct {
	VectorScore     float64 `json:"vector_score"`
	KeywordScore    float64 `json:"keyword_score"`
	GraphScore      float64 `json:"graph_score"`
	TimeScore       float64 `json:"time_score"`
	ImportanceBonus float64 `json:"importance_bonus"`
	FinalScore      float64 `json:"final_score"`
}

// MemoryStats holds aggregated statistics for a user's memory.
type MemoryStats struct {
	TotalMemories  int64                 `json:"total_memories"`
	ByLevel        map[MemoryLevel]int64 `json:"by_level"`
	TotalEntities  int64                 `json:"total_entities"`
	TotalRelations int64                 `json:"total_relations"`
	OldestMemory   time.Time             `json:"oldest_memory"`
	NewestMemory   time.Time             `json:"newest_memory"`
}
