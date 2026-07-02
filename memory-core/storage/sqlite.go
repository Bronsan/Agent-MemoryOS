package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// SQLiteSessionCache implements SessionCache using SQLite.
type SQLiteSessionCache struct {
	db *sql.DB
}

// NewSQLiteSessionCache creates a new SQLite-backed session cache.
func NewSQLiteSessionCache(path string) (*SQLiteSessionCache, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("sqlite: mkdir %s: %w", dir, err)
	}

	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000&_cache_size=-2000")
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite is single-writer

	cache := &SQLiteSessionCache{db: db}
	if err := cache.migrate(); err != nil {
		return nil, fmt.Errorf("sqlite: migrate: %w", err)
	}

	return cache, nil
}

func (s *SQLiteSessionCache) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS sessions (
			session_id    TEXT PRIMARY KEY,
			user_id       TEXT NOT NULL,
			agent_id      TEXT DEFAULT '',
			metadata      TEXT DEFAULT '{}',
			created_at    INTEGER NOT NULL,
			last_active_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_last_active ON sessions(last_active_at)`,

		`CREATE TABLE IF NOT EXISTS conversation_turns (
			id         TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			role       TEXT NOT NULL,
			content    TEXT NOT NULL,
			timestamp  INTEGER NOT NULL,
			FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_turns_session ON conversation_turns(session_id, timestamp)`,
	}

	for i, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("migration %d: %w", i, err)
		}
	}
	return nil
}

func (s *SQLiteSessionCache) StoreSession(ctx context.Context, sessionID string, data *SessionData) error {
	metaJSON, _ := json.Marshal(data.Metadata)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (session_id, user_id, agent_id, metadata, created_at, last_active_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			metadata=excluded.metadata, last_active_at=excluded.last_active_at`,
		data.SessionID, data.UserID, data.AgentID, metaJSON,
		data.CreatedAt, data.LastActiveAt,
	)
	return err
}

func (s *SQLiteSessionCache) LoadSession(ctx context.Context, sessionID string) (*SessionData, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT session_id, user_id, agent_id, metadata, created_at, last_active_at
		FROM sessions WHERE session_id = ?`, sessionID)

	data := &SessionData{}
	var metaJSON string
	err := row.Scan(&data.SessionID, &data.UserID, &data.AgentID,
		&metaJSON, &data.CreatedAt, &data.LastActiveAt)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(metaJSON), &data.Metadata)
	return data, nil
}

func (s *SQLiteSessionCache) DeleteSession(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE session_id = ?`, sessionID)
	return err
}

func (s *SQLiteSessionCache) StoreConversation(ctx context.Context, sessionID string, turn *ConversationTurn) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO conversation_turns (id, session_id, role, content, timestamp)
		VALUES (?, ?, ?, ?, ?)`,
		turn.ID, turn.SessionID, turn.Role, turn.Content, turn.Timestamp,
	)
	return err
}

func (s *SQLiteSessionCache) LoadConversation(ctx context.Context, sessionID string, limit int) ([]*ConversationTurn, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, role, content, timestamp
		FROM conversation_turns WHERE session_id = ?
		ORDER BY timestamp ASC
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []*ConversationTurn
	for rows.Next() {
		t := &ConversationTurn{}
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Role, &t.Content, &t.Timestamp); err != nil {
			return nil, err
		}
		turns = append(turns, t)
	}
	return turns, nil
}

func (s *SQLiteSessionCache) Close() error {
	return s.db.Close()
}
