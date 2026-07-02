// Package graph implements the Knowledge Graph engine.
// Entities and relations are extracted from memories and maintained as a graph
// stored in PostgreSQL for hybrid retrieval (graph traversal + vector + keyword).
package graph

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agent-memoryos/memory-core/types"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Engine manages the knowledge graph (entities + relations).
type Engine struct {
	db *sqlx.DB
}

// NewEngine creates a new knowledge graph engine.
func NewEngine(db *sqlx.DB) *Engine {
	return &Engine{db: db}
}

// UpsertEntity inserts or updates an entity by name+type uniqueness.
func (g *Engine) UpsertEntity(ctx context.Context, entity *types.Entity) error {
	if entity.ID == "" {
		entity.ID = uuid.New().String()
	}

	propsJSON, _ := json.Marshal(entity.Properties)
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO entities (id, name, type, properties, confidence)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (name, type) DO UPDATE SET
			properties = EXCLUDED.properties,
			confidence = GREATEST(entities.confidence, EXCLUDED.confidence)`,
		entity.ID, entity.Name, entity.Type, propsJSON, entity.Confidence,
	)
	return err
}

// UpsertRelation inserts or updates a relation between two entities.
func (g *Engine) UpsertRelation(ctx context.Context, rel *types.Relation) error {
	if rel.ID == "" {
		rel.ID = uuid.New().String()
	}
	_, err := g.db.ExecContext(ctx, `
		INSERT INTO relations (id, subject_id, predicate, object_id, weight, confidence, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (subject_id, predicate, object_id) DO UPDATE SET
			weight = relations.weight + EXCLUDED.weight,
			confidence = GREATEST(relations.confidence, EXCLUDED.confidence)`,
		rel.ID, rel.SubjectID, rel.Predicate, rel.ObjectID, rel.Weight, rel.Confidence, rel.Source,
	)
	return err
}

// GetEntity retrieves an entity by ID.
func (g *Engine) GetEntity(ctx context.Context, id string) (*types.Entity, error) {
	entity := &types.Entity{}
	var propsJSON string
	err := g.db.QueryRowContext(ctx,
		`SELECT id, name, type, properties, confidence FROM entities WHERE id = $1`, id,
	).Scan(&entity.ID, &entity.Name, &entity.Type, &propsJSON, &entity.Confidence)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(propsJSON), &entity.Properties)
	return entity, nil
}

// FindEntities searches entities by name prefix (fuzzy).
func (g *Engine) FindEntities(ctx context.Context, namePrefix string, limit int) ([]types.Entity, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT id, name, type, properties, confidence
		FROM entities
		WHERE name ILIKE $1
		ORDER BY confidence DESC
		LIMIT $2`, namePrefix+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entities []types.Entity
	for rows.Next() {
		var e types.Entity
		var propsJSON string
		if err := rows.Scan(&e.ID, &e.Name, &e.Type, &propsJSON, &e.Confidence); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(propsJSON), &e.Properties)
		entities = append(entities, e)
	}
	return entities, nil
}

// GetRelations retrieves all relations for a given entity.
func (g *Engine) GetRelations(ctx context.Context, entityID string) ([]types.Relation, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT id, subject_id, predicate, object_id, weight, confidence, COALESCE(source,'')
		FROM relations
		WHERE subject_id = $1 OR object_id = $1
		ORDER BY weight DESC`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var relations []types.Relation
	for rows.Next() {
		var r types.Relation
		if err := rows.Scan(&r.ID, &r.SubjectID, &r.Predicate, &r.ObjectID,
			&r.Weight, &r.Confidence, &r.Source); err != nil {
			return nil, err
		}
		relations = append(relations, r)
	}
	return relations, nil
}

// Traverse performs a graph traversal from entityIDs, returning related entity IDs
// weighted by relation strength, up to maxDepth hops.
func (g *Engine) Traverse(ctx context.Context, entityIDs []string, maxDepth int) (map[string]float64, error) {
	if maxDepth <= 0 {
		maxDepth = 2
	}

	visited := make(map[string]bool)
	scores := make(map[string]float64)
	current := make([]string, len(entityIDs))
	copy(current, entityIDs)

	for _, id := range entityIDs {
		visited[id] = true
		scores[id] = 1.0
	}

	for depth := 0; depth < maxDepth && len(current) > 0; depth++ {
		var next []string

		for _, eid := range current {
			relations, err := g.GetRelations(ctx, eid)
			if err != nil {
				continue
			}
			baseScore := scores[eid] * 0.5 // decay per hop

			for _, rel := range relations {
				var neighbor string
				if rel.SubjectID == eid {
					neighbor = rel.ObjectID
				} else {
					neighbor = rel.SubjectID
				}

				if visited[neighbor] {
					continue
				}
				visited[neighbor] = true

				score := baseScore * rel.Weight
				if existing, ok := scores[neighbor]; ok {
					scores[neighbor] = existing + score
				} else {
					scores[neighbor] = score
				}
				next = append(next, neighbor)
			}
		}
		current = next
	}

	return scores, nil
}

// GetEntityIDsForNames resolves entity names to IDs.
func (g *Engine) GetEntityIDsForNames(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}

	query, args, err := sqlx.In(
		`SELECT id FROM entities WHERE name IN (?)`, names)
	if err != nil {
		return nil, fmt.Errorf("build query: %w", err)
	}
	query = g.db.Rebind(query)

	rows, err := g.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}
