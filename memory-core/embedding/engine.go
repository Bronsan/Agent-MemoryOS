// Package embedding provides the embedding generation engine.
// It delegates all network I/O to a provider.Provider implementation,
// keeping the engine itself provider-agnostic.
package embedding

import (
	"context"
	"math"

	"github.com/agent-memoryos/memory-core/provider"
)

// Engine generates vector embeddings for text content.
type Engine struct {
	provider   provider.Provider
	dimensions int
	batchSize  int
}

// NewEngine creates a new embedding engine backed by the given provider.
func NewEngine(p provider.Provider, dimensions, batchSize int) *Engine {
	if batchSize <= 0 {
		batchSize = 32
	}
	return &Engine{
		provider:   p,
		dimensions: dimensions,
		batchSize:  batchSize,
	}
}

// Embed generates an embedding vector for a single text.
func (e *Engine) Embed(ctx context.Context, text string) ([]float32, error) {
	embeddings, err := e.provider.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, nil
	}
	return embeddings[0], nil
}

// EmbedBatch generates embeddings for multiple texts in a single call.
// Texts are automatically split into batches sized to the engine's BatchSize.
func (e *Engine) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	if len(texts) <= e.batchSize {
		return e.provider.Embed(ctx, texts)
	}

	// Large input — chunk into batches.
	result := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += e.batchSize {
		end := start + e.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		chunk, err := e.provider.Embed(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		result = append(result, chunk...)
	}
	return result, nil
}

// Dimensions returns the configured embedding dimension.
func (e *Engine) Dimensions() int {
	return e.dimensions
}

// BatchSize returns the configured batch size.
func (e *Engine) BatchSize() int {
	return e.batchSize
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
