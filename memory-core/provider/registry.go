package provider

import (
	"fmt"
	"strings"

	"github.com/agent-memoryos/memory-core/config"
)

// Registry maps a provider name (lowercase) to a factory that builds the
// provider from its generic ProviderConfig.
type Registry struct {
	factories map[string]func(ProviderConfig) (Provider, error)
}

// NewRegistry creates a Registry pre-populated with the three built-in
// providers: openai, anthropic, ollama.
func NewRegistry() *Registry {
	r := &Registry{
		factories: make(map[string]func(ProviderConfig) (Provider, error)),
	}
	r.Register("openai", func(cfg ProviderConfig) (Provider, error) {
		return NewOpenAI(OpenAIConfig{ProviderConfig: cfg}), nil
	})
	r.Register("anthropic", func(cfg ProviderConfig) (Provider, error) {
		return NewAnthropic(AnthropicConfig{ProviderConfig: cfg}), nil
	})
	r.Register("ollama", func(cfg ProviderConfig) (Provider, error) {
		return NewOllama(OllamaConfig{ProviderConfig: cfg}), nil
	})
	return r
}

// Register adds or replaces a provider factory under the given name.
// Names are normalised to lowercase.
func (r *Registry) Register(name string, factory func(ProviderConfig) (Provider, error)) {
	r.factories[strings.ToLower(name)] = factory
}

// Resolve creates a provider instance for the given name and config.
// If the provider is unknown the second return value is false.
func (r *Registry) Resolve(name string, cfg ProviderConfig) (Provider, bool, error) {
	fn, ok := r.factories[strings.ToLower(name)]
	if !ok {
		return nil, false, fmt.Errorf("provider %q not registered", name)
	}
	p, err := fn(cfg)
	if err != nil {
		return nil, true, err
	}
	return p, true, nil
}

// MustResolve calls Resolve and panics on failure. Intended for startup
// wiring where a missing provider should prevent the service from starting.
func (r *Registry) MustResolve(name string, cfg ProviderConfig) Provider {
	p, ok, err := r.Resolve(name, cfg)
	if err != nil {
		panic(fmt.Sprintf("provider %q: %v", name, err))
	}
	if !ok {
		panic(fmt.Sprintf("provider %q not registered", name))
	}
	return p
}

// NewFromEmbeddingConfig creates a provider from an EmbeddingConfig.
// This is a convenience helper so callers in main.go don't need
// to manually destructure the config.
func (r *Registry) NewFromEmbeddingConfig(cfg config.EmbeddingConfig) (Provider, error) {
	p, ok, err := r.Resolve(cfg.Provider, ProviderConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("embedding provider %q not registered", cfg.Provider)
	}
	return p, nil
}

// NewFromLLMConfig creates a provider from an LLMConfig.
func (r *Registry) NewFromLLMConfig(cfg config.LLMConfig) (Provider, error) {
	p, ok, err := r.Resolve(cfg.Provider, ProviderConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("llm provider %q not registered", cfg.Provider)
	}
	return p, nil
}

// DefaultRegistry is a package-level singleton useful for simple
// applications that only need one set of providers.
var DefaultRegistry = NewRegistry()
