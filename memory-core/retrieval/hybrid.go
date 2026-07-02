// Package retrieval implements Hybrid Search — combining vector similarity,
// keyword matching, graph traversal, temporal decay, and importance weighting.
package retrieval

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/agent-memoryos/memory-core/embedding"
	"github.com/agent-memoryos/memory-core/graph"
	"github.com/agent-memoryos/memory-core/storage"
	"github.com/agent-memoryos/memory-core/types"
)

// Engine performs hybrid retrieval across multiple signals.
type Engine struct {
	memoryStore storage.MemoryStore
	hotCache    storage.HotCache
	embEngine   *embedding.Engine
	graphEngine *graph.Engine
}

// SearchWeights configures how each retrieval signal is weighted in the final score.
type SearchWeights struct {
	Vector     float64 // 0.30
	Keyword    float64 // 0.25
	Graph      float64 // 0.20
	Time       float64 // 0.15
	Importance float64 // 0.10
}

// DefaultWeights returns balanced default weights.
func DefaultWeights() SearchWeights {
	return SearchWeights{
		Vector:     0.30,
		Keyword:    0.25,
		Graph:      0.20,
		Time:       0.15,
		Importance: 0.10,
	}
}

// NewEngine creates a new hybrid retrieval engine.
func NewEngine(
	memStore storage.MemoryStore,
	hotCache storage.HotCache,
	embEngine *embedding.Engine,
	graphEngine *graph.Engine,
) *Engine {
	return &Engine{
		memoryStore: memStore,
		hotCache:    hotCache,
		embEngine:   embEngine,
		graphEngine: graphEngine,
	}
}

// Search executes a hybrid search and returns ranked results.
func (e *Engine) Search(ctx context.Context, query *types.SearchQuery) ([]types.SearchResult, error) {
	if query.TopK <= 0 {
		query.TopK = 10
	}

	// Set default weights
	weights := SearchWeights{
		Vector:     query.VectorWeight,
		Keyword:    query.KeywordWeight,
		Graph:      query.GraphWeight,
		Time:       query.TimeWeight,
		Importance: 0.10,
	}
	if weights.Vector+weights.Keyword+weights.Graph+weights.Time == 0 {
		weights = DefaultWeights()
	}

	filters := storage.SearchFilters{
		UserID:        query.UserID,
		Levels:        query.Levels,
		MinImportance: query.MinImportance,
		TimeRange:     query.TimeRange,
	}

	// Run each search method concurrently
	var (
		wg             sync.WaitGroup
		vectorResults  []types.SearchResult
		keywordResults []types.SearchResult
		graphScores    map[string]float64
		mu             sync.Mutex
	)

	// 1. Vector Search
	if weights.Vector > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			emb := query.Embedding
			if len(emb) == 0 && query.Query != "" {
				var err error
				emb, err = e.embEngine.Embed(ctx, query.Query)
				if err != nil {
					return
				}
			}
			if len(emb) > 0 {
				results, err := e.memoryStore.SearchByVector(ctx, emb, query.TopK*2, filters)
				if err == nil {
					vectorResults = results
				}
			}
		}()
	}

	// 2. Keyword Search
	if weights.Keyword > 0 && query.Query != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := e.memoryStore.SearchByKeyword(ctx, query.Query, query.TopK*2, filters)
			if err == nil {
				keywordResults = results
			}
		}()
	}

	// 3. Graph Traversal
	if weights.Graph > 0 && len(query.Entities) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entityIDs, err := e.graphEngine.GetEntityIDsForNames(ctx, query.Entities)
			if err != nil || len(entityIDs) == 0 {
				return
			}
			scores, err := e.graphEngine.Traverse(ctx, entityIDs, 2)
			if err == nil {
				mu.Lock()
				graphScores = scores
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// Merge and score all results
	merged := e.mergeResults(ctx, vectorResults, keywordResults, graphScores, weights, query)
	sortResults(merged)

	// Truncate to TopK
	if len(merged) > query.TopK {
		merged = merged[:query.TopK]
	}

	return merged, nil
}

func (e *Engine) mergeResults(
	ctx context.Context,
	vectorResults, keywordResults []types.SearchResult,
	graphScores map[string]float64,
	weights SearchWeights,
	query *types.SearchQuery,
) []types.SearchResult {

	now := time.Now()
	candidates := make(map[string]*types.SearchResult)

	// Helper to get or create a candidate entry
	getCandidate := func(id string) *types.SearchResult {
		if c, ok := candidates[id]; ok {
			return c
		}
		c := &types.SearchResult{
			Memory: types.Memory{ID: id},
		}
		candidates[id] = c
		return c
	}

	// --- Vector results ---
	maxVecScore := 0.0
	for _, r := range vectorResults {
		c := getCandidate(r.Memory.ID)
		c.Memory = r.Memory
		vecScore := normalizeScore(r.Score, 1.0)
		c.ScoreBreakdown.VectorScore = vecScore * weights.Vector
		if vecScore > maxVecScore {
			maxVecScore = vecScore
		}
	}

	// --- Keyword results ---
	maxKWScore := 0.0
	for _, r := range keywordResults {
		c := getCandidate(r.Memory.ID)
		if c.Memory.Content == "" {
			c.Memory = r.Memory
		}
		kwScore := normalizeScore(r.Score, 1.0)
		c.ScoreBreakdown.KeywordScore = kwScore * weights.Keyword
		if kwScore > maxKWScore {
			maxKWScore = kwScore
		}
	}

	// --- Graph scores ---
	// Map entity IDs to memory IDs (simplified: graph scores boost memories with matching entities)
	for memID := range candidates {
		c := candidates[memID]
		// Boost if the memory's entities appear in graph results
		for _, entity := range c.Memory.Entities {
			if gs, ok := graphScores[entity.ID]; ok {
				c.ScoreBreakdown.GraphScore += gs * weights.Graph
			}
		}
	}

	// --- Time decay ---
	for memID, c := range candidates {
		age := now.Sub(c.Memory.LastAccessedAt).Hours() / 24  // days
		decay := math.Exp(-age * c.Memory.DecayFactor / 30.0) // 30-day half-life base
		c.ScoreBreakdown.TimeScore = decay * weights.Time

		// Importance bonus
		c.ScoreBreakdown.ImportanceBonus = c.Memory.Importance * weights.Importance

		// Final score
		c.ScoreBreakdown.FinalScore =
			c.ScoreBreakdown.VectorScore +
				c.ScoreBreakdown.KeywordScore +
				c.ScoreBreakdown.GraphScore +
				c.ScoreBreakdown.TimeScore +
				c.ScoreBreakdown.ImportanceBonus
		c.Score = c.ScoreBreakdown.FinalScore

		_ = memID
	}

	// Convert map to slice
	result := make([]types.SearchResult, 0, len(candidates))
	for _, c := range candidates {
		result = append(result, *c)
	}
	return result
}

func normalizeScore(score, max float64) float64 {
	if max == 0 {
		return 0
	}
	normalized := score / max
	if normalized > 1.0 {
		normalized = 1.0
	}
	return normalized
}

func sortResults(results []types.SearchResult) {
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
}
