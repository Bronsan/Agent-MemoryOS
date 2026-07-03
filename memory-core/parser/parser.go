// Package parser provides entity extraction and parsing from raw text.
// Supports both LLM-based extraction and rule-based regex extraction.
package parser

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/agent-memoryos/memory-core/provider"
	"github.com/agent-memoryos/memory-core/summary"
	"github.com/agent-memoryos/memory-core/types"
)

// Engine extracts entities, keywords, and structured data from text.
type Engine struct {
	llmEngine *summary.Engine
	mu        sync.RWMutex
}

// NewEngine creates a new parser engine backed by the given LLM provider.
func NewEngine(llmProvider provider.Provider, maxTokens int) *Engine {
	return &Engine{
		llmEngine: summary.NewEngine(llmProvider, maxTokens),
	}
}

// ExtractEntities extracts named entities from text using LLM.
func (e *Engine) ExtractEntities(ctx context.Context, text string) ([]types.Entity, error) {
	if len(strings.TrimSpace(text)) < 10 {
		// Rule-based extraction for very short texts
		return e.extractEntitiesRuleBased(text), nil
	}

	summaryResult, err := e.llmEngine.Summarize(ctx, text, "")
	if err != nil {
		// Fall back to rule-based if LLM fails
		return e.extractEntitiesRuleBased(text), nil
	}

	return summaryResult.Entities, nil
}

// ExtractKeywords extracts significant keywords using TF-IDF-like heuristic.
func (e *Engine) ExtractKeywords(text string) []string {
	// Simple keyword extraction: remove stop words, lowercase, split
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true, "was": true, "were": true,
		"be": true, "been": true, "being": true, "have": true, "has": true, "had": true,
		"do": true, "does": true, "did": true, "will": true, "would": true, "could": true,
		"should": true, "may": true, "might": true, "can": true, "shall": true,
		"i": true, "me": true, "my": true, "we": true, "our": true, "you": true, "your": true,
		"he": true, "him": true, "his": true, "she": true, "her": true, "it": true, "its": true,
		"they": true, "them": true, "their": true, "this": true, "that": true, "these": true,
		"those": true, "and": true, "but": true, "or": true, "not": true, "no": true,
		"in": true, "on": true, "at": true, "to": true, "for": true, "of": true, "from": true,
		"with": true, "about": true, "as": true, "by": true, "into": true, "through": true,
		"during": true, "before": true, "after": true, "above": true, "below": true,
		"up": true, "down": true, "out": true, "off": true, "over": true, "under": true,
	}

	// Clean and tokenize
	re := regexp.MustCompile(`[^a-zA-Z0-9\s-]`)
	cleaned := re.ReplaceAllString(strings.ToLower(text), " ")
	words := strings.Fields(cleaned)

	freq := make(map[string]int)
	for _, w := range words {
		if len(w) < 3 || stopWords[w] {
			continue
		}
		freq[w]++
	}

	// Sort by frequency
	type kw struct {
		word  string
		count int
	}
	var sorted []kw
	for w, c := range freq {
		sorted = append(sorted, kw{w, c})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].count > sorted[j].count
	})

	top := 20
	if len(sorted) < top {
		top = len(sorted)
	}
	result := make([]string, top)
	for i := 0; i < top; i++ {
		result[i] = sorted[i].word
	}
	return result
}

// ExtractMentions finds @mentions and #tags in text.
func (e *Engine) ExtractMentions(text string) (mentions []string, tags []string) {
	mentionRe := regexp.MustCompile(`@([a-zA-Z0-9_]+)`)
	tagRe := regexp.MustCompile(`#([a-zA-Z0-9_]+)`)

	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		if len(m) > 1 {
			mentions = append(mentions, m[1])
		}
	}
	for _, t := range tagRe.FindAllStringSubmatch(text, -1) {
		if len(t) > 1 {
			tags = append(tags, t[1])
		}
	}
	return
}

// extractEntitiesRuleBased does basic regex-based entity extraction.
func (e *Engine) extractEntitiesRuleBased(text string) []types.Entity {
	var entities []types.Entity

	// URLs
	urlRe := regexp.MustCompile(`https?://[^\s]+`)
	for _, match := range urlRe.FindAllString(text, -1) {
		entities = append(entities, types.Entity{
			Name:       match,
			Type:       types.EntityProduct,
			Confidence: 0.9,
		})
	}

	// Emails
	emailRe := regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	for _, match := range emailRe.FindAllString(text, -1) {
		entities = append(entities, types.Entity{
			Name:       match,
			Type:       types.EntityPerson,
			Confidence: 0.95,
		})
	}

	// Dates (ISO format)
	dateRe := regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	for _, match := range dateRe.FindAllString(text, -1) {
		entities = append(entities, types.Entity{
			Name:       match,
			Type:       types.EntityDate,
			Confidence: 0.95,
		})
	}

	return entities
}
