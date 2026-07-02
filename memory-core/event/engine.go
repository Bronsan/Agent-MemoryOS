// Package event implements the Event Sourcing engine.
// Every state mutation is captured as an immutable Event; all derived data
// (memories, summaries, facts, knowledge graphs) can be rebuilt from the event stream.
package event

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/agent-memoryos/memory-core/storage"
	"github.com/agent-memoryos/memory-core/types"
	"github.com/google/uuid"
)

// Engine is the core Event Sourcing engine.
type Engine struct {
	store       storage.EventStore
	handlers    map[types.EventType][]EventHandler
	projections []Projection
	mu          sync.RWMutex
}

// EventHandler is a function that reacts to a single event (side effects, async processing).
type EventHandler func(ctx context.Context, event *types.Event) error

// Projection rebuilds read-model state from the event stream.
type Projection interface {
	// Apply applies a single event to the projection state.
	Apply(ctx context.Context, event *types.Event) error
	// SubscribeTo returns the event types this projection cares about.
	SubscribeTo() []types.EventType
}

// NewEngine creates a new Event Sourcing engine.
func NewEngine(store storage.EventStore) *Engine {
	return &Engine{
		store:    store,
		handlers: make(map[types.EventType][]EventHandler),
	}
}

// RegisterHandler registers an event handler for a specific event type.
func (e *Engine) RegisterHandler(eventType types.EventType, handler EventHandler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[eventType] = append(e.handlers[eventType], handler)
}

// RegisterProjection registers a projection that rebuilds state from events.
func (e *Engine) RegisterProjection(p Projection) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.projections = append(e.projections, p)
}

// Append writes a single event to the store and dispatches to handlers.
// Returns the persisted event with its assigned ID.
func (e *Engine) Append(ctx context.Context, aggregateID string, aggregateType types.AggregateType,
	eventType types.EventType, payload types.EventPayload, meta types.EventMeta) (*types.Event, error) {

	version, err := e.store.GetLatestVersion(ctx, aggregateID, aggregateType)
	if err != nil {
		version = 0
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	evt := &types.Event{
		ID:            uuid.New().String(),
		AggregateID:   aggregateID,
		AggregateType: aggregateType,
		Type:          eventType,
		Version:       version + 1,
		Payload:       payloadJSON,
		Metadata:      meta,
		Timestamp:     time.Now().UTC(),
	}

	if err := e.store.Append(ctx, evt); err != nil {
		return nil, fmt.Errorf("append event: %w", err)
	}

	// Dispatch to handlers asynchronously
	go e.dispatch(context.Background(), evt)

	return evt, nil
}

// AppendBatch writes multiple events atomically.
func (e *Engine) AppendBatch(ctx context.Context, events []*types.Event) error {
	return e.store.Append(ctx, events...)
}

// Load retrieves the event stream for an aggregate.
func (e *Engine) Load(ctx context.Context, aggregateID string, aggregateType types.AggregateType, fromVersion int64) ([]*types.Event, error) {
	return e.store.Load(ctx, aggregateID, aggregateType, fromVersion)
}

// RebuildProjections replays all events to rebuild projection state.
func (e *Engine) RebuildProjections(ctx context.Context, aggregateID string, aggregateType types.AggregateType) error {
	events, err := e.store.Load(ctx, aggregateID, aggregateType, 0)
	if err != nil {
		return fmt.Errorf("load events: %w", err)
	}

	e.mu.RLock()
	projections := e.projections
	e.mu.RUnlock()

	for _, evt := range events {
		for _, p := range projections {
			if err := p.Apply(ctx, evt); err != nil {
				log.Printf("projection apply error: %v", err)
			}
		}
	}
	return nil
}

// dispatch sends the event to all registered handlers.
func (e *Engine) dispatch(ctx context.Context, evt *types.Event) {
	e.mu.RLock()
	handlers := e.handlers[evt.Type]
	e.mu.RUnlock()

	for _, h := range handlers {
		if err := h(ctx, evt); err != nil {
			log.Printf("event handler error for %s: %v", evt.Type, err)
		}
	}
}
