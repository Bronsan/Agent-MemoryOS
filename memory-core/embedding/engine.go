// Package embedding provides the embedding generation engine.
// Supports multiple providers: OpenAI, local models via Ollama, and custom endpoints.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/agent-memoryos/memory-core/config"
)

// Engine generates vector embeddings for text content.
type Engine struct {
	cfg        config.EmbeddingConfig
	client     *http.Client
	mu         sync.RWMutex
	dimensions int
}

// EmbeddingResponse is the standard response from an embedding API.
type EmbeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
}

// NewEngine creates a new embedding engine.
func NewEngine(cfg config.EmbeddingConfig) *Engine {
	return &Engine{
		cfg: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		dimensions: cfg.Dimensions,
	}
}

// Embed generates an embedding vector for a single text.
func (e *Engine) Embed(ctx context.Context, text string) ([]float32, error) {
	embeddings, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return embeddings[0], nil
}

// EmbedBatch generates embeddings for multiple texts in a single API call.
func (e *Engine) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	switch e.cfg.Provider {
	case "openai":
		return e.embedOpenAI(ctx, texts)
	case "ollama":
		return e.embedOllama(ctx, texts)
	case "local":
		return e.embedLocal(ctx, texts)
	default:
		return e.embedOpenAI(ctx, texts)
	}
}

func (e *Engine) embedOpenAI(ctx context.Context, texts []string) ([][]float32, error) {
	baseURL := e.cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	body := map[string]interface{}{
		"input": texts,
		"model": e.cfg.Model,
	}
	bodyJSON, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/embeddings", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embed request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai embed error %d: %s", resp.StatusCode, string(respBody))
	}

	var embResp EmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}

	result := make([][]float32, len(embResp.Data))
	for _, d := range embResp.Data {
		if d.Index < len(result) {
			result[d.Index] = d.Embedding
		}
	}
	return result, nil
}

func (e *Engine) embedOllama(ctx context.Context, texts []string) ([][]float32, error) {
	baseURL := e.cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	result := make([][]float32, 0, len(texts))
	for _, text := range texts {
		body := map[string]interface{}{
			"model":  e.cfg.Model,
			"prompt": text,
		}
		bodyJSON, _ := json.Marshal(body)

		req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/embeddings", bytes.NewReader(bodyJSON))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := e.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("ollama embed request: %w", err)
		}

		var embResp struct {
			Embedding []float32 `json:"embedding"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decode ollama response: %w", err)
		}
		resp.Body.Close()

		result = append(result, embResp.Embedding)
	}
	return result, nil
}

func (e *Engine) embedLocal(ctx context.Context, texts []string) ([][]float32, error) {
	baseURL := e.cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:8000"
	}

	body := map[string]interface{}{
		"texts": texts,
	}
	bodyJSON, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/embed", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local embed request: %w", err)
	}
	defer resp.Body.Close()

	var embResp struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decode local embedding response: %w", err)
	}
	return embResp.Embeddings, nil
}

// Dimensions returns the embedding dimension configured.
func (e *Engine) Dimensions() int {
	return e.dimensions
}

// Similarity computes cosine similarity between two vectors.
func Similarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
