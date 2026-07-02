// Package storage implements PostgreSQL + pgvector storage for Memory Core.
// All queries use parameterized statements to prevent SQL injection.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agent-memoryos/memory-core/types"
	_ "github.com/lib/pq"
)

// PostgresStore implements MemoryStore and EventStore on PostgreSQL + pgvector.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore creates a new PostgresStore and runs migrations.
func NewPostgresStore(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	store := &PostgresStore{db: db}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("postgres: migrate: %w", err)
	}

	return store, nil
}

// migrate creates the required tables, indexes, and pgvector extension.
func (p *PostgresStore) migrate() error {
	migrations := []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,

		// Events table — append-only event store
		`CREATE TABLE IF NOT EXISTS events (
			id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
			aggregate_id   UUID NOT NULL,
			aggregate_type TEXT NOT NULL,
			type       TEXT NOT NULL,
			version    BIGINT NOT NULL,
			payload    JSONB NOT NULL,
			metadata   JSONB NOT NULL DEFAULT '{}',
			timestamp  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (aggregate_id, aggregate_type, version)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_aggregate ON events(aggregate_id, aggregate_type, version)`,
		`CREATE INDEX IF NOT EXISTS idx_events_type ON events(type)`,
		`CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp)`,

		// Memories table
		`CREATE TABLE IF NOT EXISTS memories (
			id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
			user_id         UUID NOT NULL,
			agent_id        UUID,
			level           TEXT NOT NULL DEFAULT 'raw_event',
			content         TEXT NOT NULL,
			embedding       vector(1536),
			entities        JSONB DEFAULT '[]',
			relations       JSONB DEFAULT '[]',
			importance      DOUBLE PRECISION DEFAULT 0.0,
			decay_factor    DOUBLE PRECISION DEFAULT 1.0,
			access_count    BIGINT DEFAULT 0,
			last_accessed_at TIMESTAMPTZ DEFAULT NOW(),
			source_event_id UUID,
			parent_id       UUID,
			metadata        JSONB DEFAULT '{}',
			tags            TEXT[] DEFAULT '{}',
			sentiment       DOUBLE PRECISION DEFAULT 0.0,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at      TIMESTAMPTZ,
			deleted_at      TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_user_id ON memories(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_level ON memories(level)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_importance ON memories(importance)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_created_at ON memories(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_tags ON memories USING GIN(tags)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_embedding ON memories USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100)`,

		// Full-text search
		`ALTER TABLE memories ADD COLUMN IF NOT EXISTS search_vector tsvector`,
		`CREATE INDEX IF NOT EXISTS idx_memories_search ON memories USING GIN(search_vector)`,
		`CREATE OR REPLACE FUNCTION update_memory_search_vector() RETURNS TRIGGER AS $$
		BEGIN
			NEW.search_vector := to_tsvector('english', COALESCE(NEW.content, ''));
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS trg_memories_search_vector ON memories`,
		`CREATE TRIGGER trg_memories_search_vector
			BEFORE INSERT OR UPDATE ON memories
			FOR EACH ROW EXECUTE FUNCTION update_memory_search_vector()`,

		// Knowledge Graph tables
		`CREATE TABLE IF NOT EXISTS entities (
			id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
			name       TEXT NOT NULL,
			type       TEXT NOT NULL,
			properties JSONB DEFAULT '{}',
			confidence DOUBLE PRECISION DEFAULT 0.0,
			embedding  vector(1536),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_entities_name ON entities(name)`,
		`CREATE INDEX IF NOT EXISTS idx_entities_type ON entities(type)`,
		`CREATE INDEX IF NOT EXISTS idx_entities_embedding ON entities USING ivfflat (embedding vector_cosine_ops) WITH (lists = 50)`,

		`CREATE TABLE IF NOT EXISTS relations (
			id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
			subject_id UUID NOT NULL REFERENCES entities(id),
			predicate  TEXT NOT NULL,
			object_id  UUID NOT NULL REFERENCES entities(id),
			weight     DOUBLE PRECISION DEFAULT 1.0,
			confidence DOUBLE PRECISION DEFAULT 0.0,
			source     TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_relations_subject ON relations(subject_id)`,
		`CREATE INDEX IF NOT EXISTS idx_relations_object ON relations(object_id)`,
		`CREATE INDEX IF NOT EXISTS idx_relations_predicate ON relations(predicate)`,

		// Snapshots
		`CREATE TABLE IF NOT EXISTS snapshots (
			aggregate_id   UUID NOT NULL,
			aggregate_type TEXT NOT NULL,
			version        BIGINT NOT NULL,
			state          JSONB NOT NULL,
			created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (aggregate_id, aggregate_type, version)
		)`,

		// API Keys
		`CREATE TABLE IF NOT EXISTS api_keys (
			id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
			user_id     UUID NOT NULL,
			key_hash    TEXT NOT NULL UNIQUE,
			name        TEXT NOT NULL DEFAULT '',
			permissions TEXT[] DEFAULT '{}',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at  TIMESTAMPTZ,
			last_used_at TIMESTAMPTZ,
			revoked_at  TIMESTAMPTZ
		)`,
	}

	for i, m := range migrations {
		if _, err := p.db.Exec(m); err != nil {
			return fmt.Errorf("migration %d: %w", i, err)
		}
	}

	return nil
}

// Close closes the database connection.
func (p *PostgresStore) Close() error {
	return p.db.Close()
}

// DB returns the underlying *sql.DB for direct queries.
func (p *PostgresStore) DB() *sql.DB {
	return p.db
}

// --- MemoryStore implementation ---

func (p *PostgresStore) Create(ctx context.Context, memory *types.Memory) error {
	entitiesJSON, _ := json.Marshal(memory.Entities)
	relationsJSON, _ := json.Marshal(memory.Relations)
	metadataJSON, _ := json.Marshal(memory.Metadata)

	err := p.db.QueryRowContext(ctx, `
		INSERT INTO memories (user_id, agent_id, level, content, embedding, entities, relations,
			importance, decay_factor, source_event_id, parent_id, metadata, tags, sentiment, expires_at)
		VALUES ($1, $2, $3, $4, $5::vector, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		RETURNING id, created_at, updated_at`,
		memory.UserID, memory.AgentID, memory.Level, memory.Content,
		embeddingToParam(memory.Embedding),
		entitiesJSON, relationsJSON,
		memory.Importance, memory.DecayFactor, memory.SourceEventID, memory.ParentID,
		metadataJSON, pqStringArray(memory.Metadata.Tags), memory.Metadata.Sentiment, memory.ExpiresAt,
	).Scan(&memory.ID, &memory.CreatedAt, &memory.UpdatedAt)

	return err
}

func (p *PostgresStore) GetByID(ctx context.Context, id string) (*types.Memory, error) {
	row := p.db.QueryRowContext(ctx, `
		SELECT id, user_id, COALESCE(agent_id::text,''), level, content,
			COALESCE(entities::text,'[]'), COALESCE(relations::text,'[]'),
			importance, decay_factor, access_count, last_accessed_at,
			COALESCE(source_event_id::text,''), COALESCE(parent_id::text,''),
			metadata, tags, sentiment, created_at, updated_at, expires_at
		FROM memories WHERE id = $1 AND deleted_at IS NULL`, id)

	m := &types.Memory{}
	var entitiesRaw, relationsRaw, metaRaw string
	var sourceEventID, parentID, agentID sql.NullString
	var tags []string

	err := row.Scan(
		&m.ID, &m.UserID, &agentID, &m.Level, &m.Content,
		&entitiesRaw, &relationsRaw,
		&m.Importance, &m.DecayFactor, &m.AccessCount, &m.LastAccessedAt,
		&sourceEventID, &parentID,
		&metaRaw, &tags, &m.Metadata.Sentiment,
		&m.CreatedAt, &m.UpdatedAt, &m.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}

	m.AgentID = agentID.String
	m.SourceEventID = sourceEventID.String
	m.ParentID = parentID.String
	m.Metadata.Tags = tags
	json.Unmarshal([]byte(entitiesRaw), &m.Entities)
	json.Unmarshal([]byte(relationsRaw), &m.Relations)
	json.Unmarshal([]byte(metaRaw), &m.Metadata)

	// Update access metadata (best-effort, don't fail the request)
	_, _ = p.db.ExecContext(ctx,
		`UPDATE memories SET access_count = access_count + 1, last_accessed_at = NOW() WHERE id = $1`, id)

	return m, nil
}

func (p *PostgresStore) Update(ctx context.Context, memory *types.Memory) error {
	entitiesJSON, _ := json.Marshal(memory.Entities)
	relationsJSON, _ := json.Marshal(memory.Relations)
	metadataJSON, _ := json.Marshal(memory.Metadata)

	_, err := p.db.ExecContext(ctx, `
		UPDATE memories SET level=$1, content=$2, entities=$3, relations=$4,
			importance=$5, decay_factor=$6, metadata=$7, tags=$8,
			sentiment=$9, updated_at=NOW(), expires_at=$10
		WHERE id=$11 AND deleted_at IS NULL`,
		memory.Level, memory.Content, entitiesJSON, relationsJSON,
		memory.Importance, memory.DecayFactor, metadataJSON,
		pqStringArray(memory.Metadata.Tags),
		memory.Metadata.Sentiment, memory.ExpiresAt, memory.ID,
	)
	return err
}

func (p *PostgresStore) Delete(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE memories SET deleted_at = NOW() WHERE id = $1`, id)
	return err
}

// SearchByVector performs vector similarity search using pgvector.
// IMPORTANT: embedding is passed as a parameterized value — NO SQL injection.
func (p *PostgresStore) SearchByVector(ctx context.Context, embedding []float32, topK int, filters SearchFilters) ([]types.SearchResult, error) {
	// Validate topK bounds to prevent excessive resource consumption
	if topK <= 0 {
		topK = 10
	}
	if topK > 200 {
		topK = 200
	}

	embStr := embeddingToParam(embedding)

	rows, err := p.db.QueryContext(ctx, `
		SELECT id, user_id, COALESCE(agent_id::text,''), level, content,
			COALESCE(entities::text,'[]'), COALESCE(relations::text,'[]'),
			importance, decay_factor, access_count, last_accessed_at,
			COALESCE(source_event_id::text,''), COALESCE(parent_id::text,''),
			metadata, tags, sentiment, created_at, updated_at, expires_at,
			1.0 - (embedding <=> $1::vector) AS score
		FROM memories
		WHERE deleted_at IS NULL AND embedding IS NOT NULL
		ORDER BY embedding <=> $1::vector
		LIMIT $2`, embStr, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSearchResults(rows)
}

// SearchByKeyword performs full-text search via PostgreSQL tsvector.
// The query is passed as a parameterized value to plainto_tsquery — safe against injection.
func (p *PostgresStore) SearchByKeyword(ctx context.Context, query string, topK int, filters SearchFilters) ([]types.SearchResult, error) {
	if topK <= 0 {
		topK = 10
	}
	if topK > 200 {
		topK = 200
	}

	// Limit query length to prevent DoS via huge tsquery
	if len(query) > 1000 {
		query = query[:1000]
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT id, user_id, COALESCE(agent_id::text,''), level, content,
			COALESCE(entities::text,'[]'), COALESCE(relations::text,'[]'),
			importance, decay_factor, access_count, last_accessed_at,
			COALESCE(source_event_id::text,''), COALESCE(parent_id::text,''),
			metadata, tags, sentiment, created_at, updated_at, expires_at,
			ts_rank(search_vector, plainto_tsquery('english', $1)) AS score
		FROM memories
		WHERE deleted_at IS NULL AND search_vector @@ plainto_tsquery('english', $1)
		ORDER BY score DESC
		LIMIT $2`, query, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSearchResults(rows)
}

func (p *PostgresStore) ListByUser(ctx context.Context, userID string, limit, offset int, filters SearchFilters) ([]*types.Memory, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT id, user_id, COALESCE(agent_id::text,''), level, content,
			COALESCE(entities::text,'[]'), COALESCE(relations::text,'[]'),
			importance, decay_factor, access_count, last_accessed_at,
			COALESCE(source_event_id::text,''), COALESCE(parent_id::text,''),
			metadata, tags, sentiment, created_at, updated_at, expires_at
		FROM memories
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []*types.Memory
	for rows.Next() {
		m, err := scanMemoryNoScore(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, m)
	}
	return memories, nil
}

func (p *PostgresStore) GetStats(ctx context.Context, userID string) (*types.MemoryStats, error) {
	stats := &types.MemoryStats{ByLevel: make(map[types.MemoryLevel]int64)}

	row := p.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM memories WHERE user_id=$1 AND deleted_at IS NULL`, userID)
	if err := row.Scan(&stats.TotalMemories, &stats.OldestMemory, &stats.NewestMemory); err != nil {
		return nil, err
	}

	rows, err := p.db.QueryContext(ctx,
		`SELECT level, COUNT(*) FROM memories WHERE user_id=$1 AND deleted_at IS NULL GROUP BY level`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var lvl types.MemoryLevel
		var count int64
		if err := rows.Scan(&lvl, &count); err != nil {
			return nil, err
		}
		stats.ByLevel[lvl] = count
	}

	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities`).Scan(&stats.TotalEntities)
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM relations`).Scan(&stats.TotalRelations)

	return stats, nil
}

// --- EventStore implementation ---

func (p *PostgresStore) Append(ctx context.Context, events ...*types.Event) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO events (aggregate_id, aggregate_type, type, version, payload, metadata, timestamp)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range events {
		payloadJSON, _ := json.Marshal(e.Payload)
		metaJSON, _ := json.Marshal(e.Metadata)
		if _, err := stmt.ExecContext(ctx,
			e.AggregateID, e.AggregateType, e.Type, e.Version,
			payloadJSON, metaJSON, e.Timestamp,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (p *PostgresStore) Load(ctx context.Context, aggregateID string, aggregateType types.AggregateType, fromVersion int64) ([]*types.Event, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, aggregate_id, aggregate_type, type, version, payload, metadata, timestamp
		FROM events
		WHERE aggregate_id=$1 AND aggregate_type=$2 AND version >= $3
		ORDER BY version ASC`, aggregateID, aggregateType, fromVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*types.Event
	for rows.Next() {
		e := &types.Event{}
		var payloadRaw, metaRaw []byte
		if err := rows.Scan(&e.ID, &e.AggregateID, &e.AggregateType, &e.Type,
			&e.Version, &payloadRaw, &metaRaw, &e.Timestamp); err != nil {
			return nil, err
		}
		e.Payload = payloadRaw
		json.Unmarshal(metaRaw, &e.Metadata)
		events = append(events, e)
	}
	return events, nil
}

func (p *PostgresStore) GetLatestVersion(ctx context.Context, aggregateID string, aggregateType types.AggregateType) (int64, error) {
	var version sql.NullInt64
	err := p.db.QueryRowContext(ctx, `
		SELECT MAX(version) FROM events WHERE aggregate_id=$1 AND aggregate_type=$2`,
		aggregateID, aggregateType).Scan(&version)
	if err != nil {
		return 0, err
	}
	return version.Int64, nil
}

// Subscribe returns a channel for new events (placeholder — uses LISTEN/NOTIFY in production).
func (p *PostgresStore) Subscribe(ctx context.Context, filter EventFilter) (<-chan *types.Event, error) {
	ch := make(chan *types.Event, 100)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// --- helpers ---

// embeddingToParam converts a float32 slice to the pgvector-compatible string format.
// The result is still passed as a parameterized query value ($N), NOT string interpolation.
func embeddingToParam(embedding []float32) string {
	if len(embedding) == 0 {
		return "[]"
	}
	parts := make([]string, len(embedding))
	for i, v := range embedding {
		parts[i] = fmt.Sprintf("%.6f", v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// pqStringArray converts a Go string slice to a PostgreSQL array literal for parameterized queries.
func pqStringArray(s []string) interface{} {
	if s == nil {
		return "{}"
	}
	return "{" + strings.Join(escapeEach(s), ",") + "}"
}

func escapeEach(ss []string) []string {
	result := make([]string, len(ss))
	for i, s := range ss {
		// Escape double quotes and backslashes for PostgreSQL array literals
		s = strings.ReplaceAll(s, "\\", "\\\\")
		s = strings.ReplaceAll(s, "\"", "\\\"")
		result[i] = "\"" + s + "\""
	}
	return result
}

// --- scan helpers ---

func scanSearchResults(rows *sql.Rows) ([]types.SearchResult, error) {
	var results []types.SearchResult
	for rows.Next() {
		m, score, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, types.SearchResult{
			Memory: *m,
			Score:  score,
			ScoreBreakdown: types.ScoreBreakdown{
				FinalScore: score,
			},
		})
	}
	return results, nil
}

func scanMemory(rows *sql.Rows) (*types.Memory, float64, error) {
	m := &types.Memory{}
	var entitiesRaw, relationsRaw, metaRaw string
	var sourceEventID, parentID, agentID sql.NullString
	var tags []string
	var score float64

	err := rows.Scan(
		&m.ID, &m.UserID, &agentID, &m.Level, &m.Content,
		&entitiesRaw, &relationsRaw,
		&m.Importance, &m.DecayFactor, &m.AccessCount, &m.LastAccessedAt,
		&sourceEventID, &parentID,
		&metaRaw, &tags, &m.Metadata.Sentiment,
		&m.CreatedAt, &m.UpdatedAt, &m.ExpiresAt,
		&score,
	)
	if err != nil {
		return nil, 0, err
	}

	m.AgentID = agentID.String
	m.SourceEventID = sourceEventID.String
	m.ParentID = parentID.String
	m.Metadata.Tags = tags
	json.Unmarshal([]byte(entitiesRaw), &m.Entities)
	json.Unmarshal([]byte(relationsRaw), &m.Relations)
	json.Unmarshal([]byte(metaRaw), &m.Metadata)

	return m, score, nil
}

func scanMemoryNoScore(rows *sql.Rows) (*types.Memory, error) {
	m, _, err := scanMemory(rows)
	return m, err
}
