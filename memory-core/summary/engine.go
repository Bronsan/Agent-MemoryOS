// Package summary provides the Summary Engine — transforms raw events
// into concise episode/fact summaries using LLM-based distillation.
package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/agent-memoryos/memory-core/config"
	"github.com/agent-memoryos/memory-core/types"
)

// Engine generates summaries and extracts structured knowledge from raw text.
type Engine struct {
	cfg    config.LLMConfig
	client *http.Client
}

// SummaryResult holds the LLM-generated summary output.
type SummaryResult struct {
	EpisodeSummary   string         `json:"episode_summary"`
	Facts            []string       `json:"facts"`
	Preferences      []string       `json:"preferences"`
	PersonalityNotes []string       `json:"personality_notes"`
	KnowledgeItems   []string       `json:"knowledge_items"`
	Entities         []types.Entity `json:"entities"`
	Importance       float64        `json:"importance"`
}

// NewEngine creates a new summary engine.
func NewEngine(cfg config.LLMConfig) *Engine {
	return &Engine{
		cfg: cfg,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Summarize generates a structured summary from raw conversation/memory text.
func (e *Engine) Summarize(ctx context.Context, text string, previousContext string) (*SummaryResult, error) {
	prompt := e.buildPrompt(text, previousContext)
	return e.callLLM(ctx, prompt)
}

// SummarizeBatch summarizes multiple memory fragments together.
func (e *Engine) SummarizeBatch(ctx context.Context, texts []string, previousContext string) (*SummaryResult, error) {
	combined := ""
	for i, t := range texts {
		if i > 0 {
			combined += "\n---\n"
		}
		combined += t
	}
	return e.Summarize(ctx, combined, previousContext)
}

func (e *Engine) buildPrompt(text string, previousContext string) string {
	prompt := `You are an advanced memory distillation engine. Your task is to analyze the input and extract structured memory artifacts.

Input Text:
"""
%s
"""`

	if previousContext != "" {
		prompt += fmt.Sprintf(`

Previous Context:
"""
%s
"""`, previousContext)
	}

	prompt += `

Return a JSON object with the following fields:
- "episode_summary": A concise 2-3 sentence summary of what happened.
- "facts": Array of objective facts extracted from the text.
- "preferences": Array of user preferences or likes/dislikes detected.
- "personality_notes": Array of observations about the user's personality traits.
- "knowledge_items": Array of general knowledge items mentioned.
- "entities": Array of { "name": "...", "type": "person|organization|location|product|concept", "confidence": 0.0-1.0 }.
- "importance": A score from 0.0 to 1.0 indicating how important this memory is to retain long-term.

Only return the JSON object, no other text.`

	return fmt.Sprintf(prompt, text)
}

func (e *Engine) callLLM(ctx context.Context, prompt string) (*SummaryResult, error) {
	baseURL := e.cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	body := map[string]interface{}{
		"model": e.cfg.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens":  e.cfg.MaxTokens,
		"temperature": 0.1, // low temperature for consistent extraction
		"response_format": map[string]string{
			"type": "json_object",
		},
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
		return nil, fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llm error %d: %s", resp.StatusCode, string(respBody))
	}

	var llmResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&llmResp); err != nil {
		return nil, fmt.Errorf("decode llm response: %w", err)
	}

	if len(llmResp.Choices) == 0 {
		return nil, fmt.Errorf("empty llm response")
	}

	content := llmResp.Choices[0].Message.Content

	var result SummaryResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("parse summary json: %w\nraw: %s", err, content)
	}

	return &result, nil
}
