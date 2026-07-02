// Package reranker re-ranks search results using a cross-encoder or LLM-based
// relevance model to improve retrieval precision.
package reranker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/agent-memoryos/memory-core/config"
	"github.com/agent-memoryos/memory-core/types"
)

// Engine re-ranks search results for better relevance.
type Engine struct {
	cfg    config.LLMConfig
	client *http.Client
}

// NewEngine creates a new reranker engine.
func NewEngine(cfg config.LLMConfig) *Engine {
	return &Engine{
		cfg: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Rerank takes a query and a list of results, and returns re-ranked results.
// Uses the LLM as a pointwise relevance judge.
func (e *Engine) Rerank(ctx context.Context, query string, results []types.SearchResult, topK int) ([]types.SearchResult, error) {
	if len(results) <= 1 || topK >= len(results) {
		return results, nil
	}

	scores, err := e.scoreBatch(ctx, query, results)
	if err != nil {
		// If reranking fails, fall back to original order
		return results[:min(topK, len(results))], nil
	}

	// Apply reranker scores as multipliers to original scores
	for i := range results {
		if i < len(scores) {
			results[i].Score = results[i].Score * (0.5 + 0.5*scores[i])
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if topK < len(results) {
		results = results[:topK]
	}

	return results, nil
}

func (e *Engine) scoreBatch(ctx context.Context, query string, results []types.SearchResult) ([]float64, error) {
	// Build a prompt that asks the LLM to score relevance
	var sb strings.Builder
	sb.WriteString("Query: ")
	sb.WriteString(query)
	sb.WriteString("\n\nRate the relevance of each document to the query on a scale of 0.0 (completely irrelevant) to 1.0 (perfectly relevant).\n\n")

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("Document %d:\n%s\n\n", i, truncate(r.Memory.Content, 500)))
	}

	sb.WriteString("Return a JSON array of scores, one per document: [score0, score1, ...]")

	return e.callReranker(ctx, sb.String(), len(results))
}

func (e *Engine) callReranker(ctx context.Context, prompt string, numDocs int) ([]float64, error) {
	baseURL := e.cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	body := map[string]interface{}{
		"model": e.cfg.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens":  256,
		"temperature": 0.0,
	}

	bodyJSON, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reranker request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("reranker error %d: %s", resp.StatusCode, string(respBody))
	}

	var llmResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&llmResp); err != nil {
		return nil, err
	}

	if len(llmResp.Choices) == 0 {
		return nil, fmt.Errorf("empty reranker response")
	}

	content := strings.TrimSpace(llmResp.Choices[0].Message.Content)

	var scores []float64
	if err := json.Unmarshal([]byte(content), &scores); err != nil {
		return nil, fmt.Errorf("parse reranker scores: %w", err)
	}

	return scores, nil
}

// RerankWithThreshold filters and re-ranks results, applying a minimum score threshold.
func (e *Engine) RerankWithThreshold(ctx context.Context, query string, results []types.SearchResult, topK int, threshold float64) ([]types.SearchResult, error) {
	reranked, err := e.Rerank(ctx, query, results, topK)
	if err != nil {
		return nil, err
	}

	var filtered []types.SearchResult
	for _, r := range reranked {
		if r.Score >= threshold {
			filtered = append(filtered, r)
		}
	}

	if len(filtered) == 0 {
		// Return at least the top result if nothing meets threshold
		if len(reranked) > 0 {
			return reranked[:1], nil
		}
	}

	return filtered, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
