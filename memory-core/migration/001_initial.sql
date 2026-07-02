-- Migration 001: Initial Schema
-- This SQL must be run against a PostgreSQL instance with the pgvector extension available.
-- Run: psql -U memoryos -d memoryos -f 001_initial.sql

BEGIN;

-- Enable required extensions
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ────────────────────────────────────────────────────────────────────
-- Event Store (Event Sourcing — append-only)
-- ────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS events (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    aggregate_id    UUID NOT NULL,
    aggregate_type  TEXT NOT NULL,
    type            TEXT NOT NULL,
    version         BIGINT NOT NULL,
    payload         JSONB NOT NULL,
    metadata        JSONB NOT NULL DEFAULT '{}',
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (aggregate_id, aggregate_type, version)
);

CREATE INDEX IF NOT EXISTS idx_events_aggregate
    ON events (aggregate_id, aggregate_type, version);
CREATE INDEX IF NOT EXISTS idx_events_type ON events (type);
CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events (timestamp);

-- ────────────────────────────────────────────────────────────────────
-- Memories (primary memory store with pgvector embedding)
-- ────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS memories (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id           UUID NOT NULL,
    agent_id          UUID,
    level             TEXT NOT NULL DEFAULT 'raw_event',
    content           TEXT NOT NULL,
    embedding         vector(1536),
    entities          JSONB DEFAULT '[]',
    relations         JSONB DEFAULT '[]',
    importance        DOUBLE PRECISION DEFAULT 0.0,
    decay_factor      DOUBLE PRECISION DEFAULT 1.0,
    access_count      BIGINT DEFAULT 0,
    last_accessed_at  TIMESTAMPTZ DEFAULT NOW(),
    source_event_id   UUID,
    parent_id         UUID,
    metadata          JSONB DEFAULT '{}',
    tags              TEXT[] DEFAULT '{}',
    sentiment         DOUBLE PRECISION DEFAULT 0.0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at        TIMESTAMPTZ,
    deleted_at        TIMESTAMPTZ
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_memories_user_id ON memories (user_id);
CREATE INDEX IF NOT EXISTS idx_memories_level ON memories (level);
CREATE INDEX IF NOT EXISTS idx_memories_importance ON memories (importance);
CREATE INDEX IF NOT EXISTS idx_memories_created_at ON memories (created_at);
CREATE INDEX IF NOT EXISTS idx_memories_tags ON memories USING GIN (tags);

-- pgvector IVFFlat index for vector similarity search
CREATE INDEX IF NOT EXISTS idx_memories_embedding
    ON memories USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);

-- Full-text search via tsvector
ALTER TABLE memories ADD COLUMN IF NOT EXISTS search_vector tsvector;
CREATE INDEX IF NOT EXISTS idx_memories_search ON memories USING GIN (search_vector);

CREATE OR REPLACE FUNCTION update_memory_search_vector()
RETURNS TRIGGER AS $$
BEGIN
    NEW.search_vector := to_tsvector('english', COALESCE(NEW.content, ''));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_memories_search_vector ON memories;
CREATE TRIGGER trg_memories_search_vector
    BEFORE INSERT OR UPDATE ON memories
    FOR EACH ROW EXECUTE FUNCTION update_memory_search_vector();

-- ────────────────────────────────────────────────────────────────────
-- Knowledge Graph: Entities & Relations
-- ────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS entities (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        TEXT NOT NULL,
    type        TEXT NOT NULL,
    properties  JSONB DEFAULT '{}',
    confidence  DOUBLE PRECISION DEFAULT 0.0,
    embedding   vector(1536),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (name, type)
);

CREATE INDEX IF NOT EXISTS idx_entities_name ON entities (name);
CREATE INDEX IF NOT EXISTS idx_entities_type ON entities (type);
CREATE INDEX IF NOT EXISTS idx_entities_embedding
    ON entities USING ivfflat (embedding vector_cosine_ops) WITH (lists = 50);

CREATE TABLE IF NOT EXISTS relations (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    subject_id  UUID NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    predicate   TEXT NOT NULL,
    object_id   UUID NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    weight      DOUBLE PRECISION DEFAULT 1.0,
    confidence  DOUBLE PRECISION DEFAULT 0.0,
    source      TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (subject_id, predicate, object_id)
);

CREATE INDEX IF NOT EXISTS idx_relations_subject ON relations (subject_id);
CREATE INDEX IF NOT EXISTS idx_relations_object ON relations (object_id);
CREATE INDEX IF NOT EXISTS idx_relations_predicate ON relations (predicate);

-- ────────────────────────────────────────────────────────────────────
-- Snapshots (Event Sourcing performance optimization)
-- ────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS snapshots (
    aggregate_id    UUID NOT NULL,
    aggregate_type  TEXT NOT NULL,
    version         BIGINT NOT NULL,
    state           JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (aggregate_id, aggregate_type, version)
);

-- ────────────────────────────────────────────────────────────────────
-- API Keys (Authentication)
-- ────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS api_keys (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     UUID NOT NULL,
    key_hash    TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL DEFAULT '',
    permissions TEXT[] DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys (user_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys (key_hash);

-- ────────────────────────────────────────────────────────────────────
-- Composite Index for Common Hybrid Search Queries
-- ────────────────────────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_memories_hybrid_search
    ON memories (user_id, level, importance DESC, created_at DESC)
    WHERE deleted_at IS NULL;

COMMIT;
