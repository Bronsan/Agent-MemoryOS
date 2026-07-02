// Package plugins provides the plugin system for ingesting data from external sources.
// Plugins implement the SourcePlugin interface and are loaded dynamically.
package plugins

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/types"
	"github.com/google/uuid"
)

// SourcePlugin is the interface that all data-source plugins must implement.
type SourcePlugin interface {
	// Name returns the unique plugin identifier.
	Name() string

	// Start begins listening for data from the source.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the plugin.
	Stop() error

	// Health checks if the plugin is operational.
	Health(ctx context.Context) error
}

// Registry manages all loaded plugins.
type Registry struct {
	plugins     map[string]SourcePlugin
	eventEngine *event.Engine
	mu          sync.RWMutex
}

// NewRegistry creates a new plugin registry.
func NewRegistry(eventEngine *event.Engine) *Registry {
	return &Registry{
		plugins:     make(map[string]SourcePlugin),
		eventEngine: eventEngine,
	}
}

// Register adds a plugin to the registry.
func (r *Registry) Register(plugin SourcePlugin) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := plugin.Name()
	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("plugin %s already registered", name)
	}

	r.plugins[name] = plugin
	log.Printf("plugin registered: %s", name)
	return nil
}

// Unregister removes a plugin from the registry.
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	plugin, exists := r.plugins[name]
	if !exists {
		return fmt.Errorf("plugin %s not found", name)
	}

	if err := plugin.Stop(); err != nil {
		log.Printf("plugin %s stop error: %v", name, err)
	}

	delete(r.plugins, name)
	log.Printf("plugin unregistered: %s", name)
	return nil
}

// StartAll starts all registered plugins.
func (r *Registry) StartAll(ctx context.Context) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for name, plugin := range r.plugins {
		if err := plugin.Start(ctx); err != nil {
			return fmt.Errorf("plugin %s start: %w", name, err)
		}
		log.Printf("plugin started: %s", name)
	}
	return nil
}

// StopAll stops all registered plugins.
func (r *Registry) StopAll() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for name, plugin := range r.plugins {
		if err := plugin.Stop(); err != nil {
			log.Printf("plugin %s stop error: %v", name, err)
		}
	}
}

// List returns the names of all registered plugins.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.plugins))
	for name := range r.plugins {
		names = append(names, name)
	}
	return names
}

// IngestToEvent is a helper that plugins use to convert external data into events.
func IngestToEvent(
	ctx context.Context,
	eventEngine *event.Engine,
	userID, agentID, sessionID, source, text string,
) (*types.Event, error) {
	payload := types.RawInputPayload{
		Text:   text,
		Format: "text",
	}
	meta := types.EventMeta{
		Source:    source,
		UserID:    userID,
		AgentID:   agentID,
		SessionID: sessionID,
	}
	return eventEngine.Append(ctx, uuid.New().String(), types.AggregateMemory,
		types.EventRawInput, payload, meta)
}
