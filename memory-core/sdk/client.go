// Package sdk provides the Go SDK for Memory Core.
// Allows any LLM or Agent application to integrate memory capabilities.
package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/agent-memoryos/memory-core/types"
)

// Client is the Go SDK client for the Memory Core API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// ClientOption configures the SDK client.
type ClientOption func(*Client)

// WithTimeout sets the HTTP client timeout.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		c.httpClient.Timeout = d
	}
}

// WithHTTPClient sets a custom HTTP client (e.g., with TLS config).
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = client
	}
}

// NewClient creates a new Memory Core SDK client.
// baseURL should be the full server URL, e.g., "http://localhost:8080" or "https://memory.example.com".
func NewClient(baseURL, apiKey string, opts ...ClientOption) *Client {
	c := &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Ingest writes a raw memory event (fast path).
func (c *Client) Ingest(ctx context.Context, req *IngestInput) (*IngestOutput, error) {
	body := map[string]interface{}{
		"user_id":    req.UserID,
		"agent_id":   req.AgentID,
		"session_id": req.SessionID,
		"text":       req.Text,
		"source":     req.Source,
		"metadata":   req.Metadata,
	}

	var result struct {
		EventID  string `json:"event_id"`
		MemoryID string `json:"memory_id"`
		Status   string `json:"status"`
	}

	if err := c.do(ctx, "POST", "/api/v1/ingest", body, &result); err != nil {
		return nil, err
	}

	return &IngestOutput{
		EventID:  result.EventID,
		MemoryID: result.MemoryID,
		Status:   result.Status,
	}, nil
}

// Search performs hybrid search across memories.
func (c *Client) Search(ctx context.Context, req *SearchInput) (*SearchOutput, error) {
	if req.TopK <= 0 {
		req.TopK = 10
	}
	if req.TopK > 100 {
		req.TopK = 100
	}

	body := map[string]interface{}{
		"user_id":        req.UserID,
		"query":          req.Query,
		"keywords":       req.Keywords,
		"entities":       req.Entities,
		"levels":         req.Levels,
		"top_k":          req.TopK,
		"min_importance": req.MinImportance,
	}

	var result struct {
		Results []types.SearchResult `json:"results"`
		TookMs  int64                `json:"took_ms"`
	}

	if err := c.do(ctx, "POST", "/api/v1/search", body, &result); err != nil {
		return nil, err
	}

	return &SearchOutput{
		Results: result.Results,
		TookMs:  result.TookMs,
	}, nil
}

// GetMemory retrieves a single memory by ID.
func (c *Client) GetMemory(ctx context.Context, memoryID string) (*types.Memory, error) {
	// Validate and encode the memory ID to prevent path traversal / injection
	if memoryID == "" {
		return nil, fmt.Errorf("memory id is required")
	}

	path := "/api/v1/memories/" + url.PathEscape(memoryID)

	var result struct {
		Memory *types.Memory `json:"memory"`
	}
	if err := c.do(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}
	return result.Memory, nil
}

// GetStats returns memory statistics for a user.
func (c *Client) GetStats(ctx context.Context, userID string) (*types.MemoryStats, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}

	// Properly encode query parameters
	path := "/api/v1/stats?user_id=" + url.QueryEscape(userID)

	var result struct {
		Stats *types.MemoryStats `json:"stats"`
	}
	if err := c.do(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}
	return result.Stats, nil
}

// Health checks if the service is healthy.
func (c *Client) Health(ctx context.Context) (bool, error) {
	var result map[string]string
	if err := c.do(ctx, "GET", "/health", nil, &result); err != nil {
		return false, err
	}
	return result["status"] == "ok", nil
}

// --- SDK input/output types ---

// IngestInput represents a memory ingestion request.
type IngestInput struct {
	UserID    string                 `json:"user_id"`
	AgentID   string                 `json:"agent_id,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	Text      string                 `json:"text"`
	Source    string                 `json:"source"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// IngestOutput is the response from a memory ingestion.
type IngestOutput struct {
	EventID  string `json:"event_id"`
	MemoryID string `json:"memory_id"`
	Status   string `json:"status"`
}

// SearchInput represents a hybrid search request.
type SearchInput struct {
	UserID        string   `json:"user_id"`
	Query         string   `json:"query"`
	Keywords      []string `json:"keywords,omitempty"`
	Entities      []string `json:"entities,omitempty"`
	Levels        []string `json:"levels,omitempty"`
	TopK          int      `json:"top_k"`
	MinImportance float64  `json:"min_importance,omitempty"`
}

// SearchOutput is the response from a hybrid search.
type SearchOutput struct {
	Results []types.SearchResult `json:"results"`
	TookMs  int64                `json:"took_ms"`
}

// --- HTTP helpers ---

func (c *Client) do(ctx context.Context, method, path string, body, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("sdk: marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("sdk: create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sdk: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("sdk: api error %d: %s", resp.StatusCode, string(respBody))
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("sdk: decode response: %w", err)
		}
	}

	return nil
}
