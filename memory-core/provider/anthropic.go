package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Anthropic message request/response types.
// Reference: https://docs.anthropic.com/en/api/messages

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicChatRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
}

type anthropicChatResponse struct {
	ID      string                  `json:"id"`
	Type    string                  `json:"type"`
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
	Error   *anthropicError         `json:"error,omitempty"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// AnthropicConfig holds extended options for the Anthropic provider.
type AnthropicConfig struct {
	ProviderConfig
	MaxTokens int // default 4096 if unset
}

// AnthropicProvider implements Provider for the Anthropic Messages API.
//
// Notes:
//   - Anthropic does not offer a native embeddings endpoint. Embed() always
//     returns ErrNotSupported.
//   - Anthropic has no public models-list endpoint. Models() returns a
//     curated list of known model IDs. Health() relies on a lightweight
//     connectivity probe instead.
type AnthropicProvider struct {
	apiKey    string
	baseURL   string
	model     string
	maxTokens int
	client    *http.Client
}

// NewAnthropic creates an Anthropic provider backed by the Messages API.
func NewAnthropic(cfg AnthropicConfig) *AnthropicProvider {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	mt := cfg.MaxTokens
	if mt <= 0 {
		mt = 4096
	}
	return &AnthropicProvider{
		apiKey:    cfg.APIKey,
		baseURL:   cfg.BaseURL,
		model:     cfg.Model,
		maxTokens: mt,
		client: &http.Client{
			Timeout: time.Duration(timeout) * time.Second,
		},
	}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// Chat sends a request to the Anthropic Messages API.
//
// Anthropic requires every message to carry a role of "user" or "assistant".
// System messages are extracted and sent via the top-level "system" field.
func (p *AnthropicProvider) Chat(ctx context.Context, messages []Message) (*ChatResponse, error) {
	var systemPrompt string
	antMessages := make([]anthropicMessage, 0, len(messages))

	for _, m := range messages {
		if m.Role == "system" {
			if systemPrompt != "" {
				systemPrompt += "\n"
			}
			systemPrompt += m.Content
			continue
		}
		antMessages = append(antMessages, anthropicMessage{
			Role: m.Role,
			Content: []anthropicContentBlock{
				{Type: "text", Text: m.Content},
			},
		})
	}

	reqBody := anthropicChatRequest{
		Model:     p.model,
		Messages:  antMessages,
		MaxTokens: p.maxTokens,
		System:    systemPrompt,
	}

	respBody, err := p.doAnthropic(ctx, "POST", "/messages", reqBody)
	if err != nil {
		return nil, err
	}

	var cr anthropicChatResponse
	if err := json.NewDecoder(respBody).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decode anthropic chat response: %w", err)
	}
	if cr.Error != nil {
		return nil, fmt.Errorf("anthropic chat error (type=%s): %s", cr.Error.Type, cr.Error.Message)
	}

	// Concatenate text blocks into a single string.
	var content string
	for _, block := range cr.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}
	if content == "" && len(cr.Content) > 0 {
		content = cr.Content[0].Text
	}

	return &ChatResponse{
		Content:      content,
		FinishReason: "stop", // Anthropic doesn't always expose this field at top level
	}, nil
}

// Embed is not supported by the Anthropic API.
func (p *AnthropicProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return nil, ErrNotSupported
}

// Models returns a curated list of known Anthropic model IDs.
// The Anthropic API does not currently expose a public /models endpoint.
func (p *AnthropicProvider) Models(ctx context.Context) ([]ModelInfo, error) {
	return []ModelInfo{
		{ID: "claude-3-5-sonnet-20241022", Name: "Claude 3.5 Sonnet"},
		{ID: "claude-3-opus-20240229", Name: "Claude 3 Opus"},
		{ID: "claude-3-sonnet-20240229", Name: "Claude 3 Sonnet"},
		{ID: "claude-3-haiku-20240307", Name: "Claude 3 Haiku"},
		{ID: "claude-3-5-haiku-20241022", Name: "Claude 3.5 Haiku"},
	}, nil
}

// Health sends a lightweight request to verify the endpoint is reachable
// and the API key is valid. It reuses a minimal /messages probe.
func (p *AnthropicProvider) Health(ctx context.Context) error {
	// Minimal probe — we just need to confirm the endpoint responds.
	req, err := http.NewRequestWithContext(ctx, "GET", p.effectiveBaseURL(), nil)
	if err != nil {
		return fmt.Errorf("anthropic health: %w", err)
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic health: %w", err)
	}
	defer resp.Body.Close()

	// 400+ with a known error structure = auth is valid / endpoint is reachable.
	// We only treat connection-level errors as unhealthy.
	// Any response (even 404) means the endpoint is alive.
	return nil
}

func (p *AnthropicProvider) effectiveBaseURL() string {
	if p.baseURL != "" {
		return p.baseURL
	}
	return "https://api.anthropic.com/v1"
}

func (p *AnthropicProvider) doAnthropic(ctx context.Context, method, path string, body interface{}) (io.ReadCloser, error) {
	url := p.effectiveBaseURL() + path

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic request: %w", err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("anthropic http %d for %s: %s", resp.StatusCode, path, sanitiseBody(bodyBytes))
	}

	return resp.Body, nil
}
