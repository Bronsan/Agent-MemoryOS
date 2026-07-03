// Package summary provides the Summary Engine — transforms raw events
// into concise episode/fact summaries using LLM-based distillation.
package summary

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agent-memoryos/memory-core/provider"
	"github.com/agent-memoryos/memory-core/types"
)

// Engine generates summaries and extracts structured knowledge from raw text.
type Engine struct {
	provider  provider.Provider
	maxTokens int
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

// NewEngine creates a new summary engine backed by the given provider.
func NewEngine(p provider.Provider, maxTokens int) *Engine {
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	return &Engine{
		provider:  p,
		maxTokens: maxTokens,
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
	messages := []provider.Message{
		{Role: "user", Content: prompt},
	}

	resp, err := e.provider.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("llm call: %w", err)
	}

	var result SummaryResult
	if err := json.Unmarshal([]byte(resp.Content), &result); err != nil {
		return nil, fmt.Errorf("parse summary json: %w\nraw: %s", err, truncate(resp.Content, 500))
	}

	return &result, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
