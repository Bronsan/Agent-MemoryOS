// Package provider defines a unified LLM/embedding provider abstraction,
// enabling hot-swappable model backends with consistent safety guarantees.
package provider

import (
	"context"
	"errors"
	"fmt"
)

// Message represents a chat message in a conversation.
type Message struct {
	Role    string `json:"role"`    // "system", "user", "assistant"
	Content string `json:"content"` // message body
}

// ChatResponse wraps the assistant's reply from a Chat call.
type ChatResponse struct {
	Content      string `json:"content"`
	FinishReason string `json:"finish_reason,omitempty"`
}

// ModelInfo describes a model available from the provider.
type ModelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// ProviderConfig holds the configuration needed to initialise a provider.
// APIKey is intentionally a plain field — the caller is responsible for
// reading it from a secure source (env var, vault, etc.) and never logging it.
type ProviderConfig struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout int // seconds; 0 means use provider default (30s)
}

// ErrNotSupported is returned when an operation is not supported by a provider
// (e.g. Anthropic does not offer a native embeddings API).
var ErrNotSupported = errors.New("provider: operation not supported")

// SanitisedError strips an API key (if present) from an error message so that
// it can be safely logged or returned to callers.
func SanitisedError(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	// Generic scrub: mask anything that looks like "Bearer sk-..." or "x-api-key: ..."
	// Kept intentionally simple — no regex dependency.
	return fmt.Errorf("provider error: %s", msg)
}

// Provider is the core abstraction over a model backend.
// Every implementation MUST ensure:
//   - API keys are NEVER included in log output or error messages.
//   - All network calls respect the attached context deadline.
//   - Response bodies are closed and fully consumed before returning.
type Provider interface {
	// Chat sends a conversation and returns the assistant's reply.
	// The provider attaches its configured model and auth headers internally.
	Chat(ctx context.Context, messages []Message) (*ChatResponse, error)

	// Embed returns vector embeddings for the supplied texts.
	// May return ErrNotSupported for providers that lack an embeddings API.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// Models lists the models available through this provider.
	Models(ctx context.Context) ([]ModelInfo, error)

	// Health performs a lightweight connectivity check.
	// Returns nil when the endpoint is reachable and auth (if required) is valid.
	Health(ctx context.Context) error

	// Name returns a human-readable provider identifier (e.g. "openai", "anthropic", "ollama").
	Name() string
}
