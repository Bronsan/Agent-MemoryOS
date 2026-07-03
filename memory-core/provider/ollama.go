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

// Ollama request/response types (v1 API).
// Reference: https://github.com/ollama/ollama/blob/main/docs/api.md

type ollamaChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type ollamaChatResponse struct {
	Model      string  `json:"model"`
	Message    Message `json:"message"`
	Done       bool    `json:"done"`
	DoneReason string  `json:"done_reason,omitempty"`
}

type ollamaEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
}

type ollamaTagsResponse struct {
	Models []ollamaModel `json:"models"`
}

type ollamaModel struct {
	Name string `json:"name"`
}

// OllamaConfig holds extended options for the Ollama provider.
type OllamaConfig struct {
	ProviderConfig
}

// OllamaProvider implements Provider for a local Ollama server.
// Ollama typically runs without authentication on localhost:11434.
type OllamaProvider struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewOllama creates an Ollama provider.
// If timeout is 0, defaults to 60 s (local models can be slower on first load).
func NewOllama(cfg OllamaConfig) *OllamaProvider {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60
	}
	return &OllamaProvider{
		baseURL: cfg.BaseURL,
		model:   cfg.Model,
		client: &http.Client{
			Timeout: time.Duration(timeout) * time.Second,
		},
	}
}

func (p *OllamaProvider) Name() string { return "ollama" }

// Chat sends a chat completion request to Ollama.
func (p *OllamaProvider) Chat(ctx context.Context, messages []Message) (*ChatResponse, error) {
	body := ollamaChatRequest{
		Model:    p.model,
		Messages: messages,
		Stream:   false,
	}
	resp, err := p.doOllama(ctx, "POST", "/api/chat", body)
	if err != nil {
		return nil, err
	}

	var cr ollamaChatResponse
	if err := json.NewDecoder(resp).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decode ollama chat response: %w", err)
	}
	return &ChatResponse{
		Content:      cr.Message.Content,
		FinishReason: cr.DoneReason,
	}, nil
}

// Embed generates embeddings via Ollama's /api/embeddings endpoint.
// Ollama processes one text at a time; concurrency is handled by the caller
// (the embedding engine can batch individual calls).
func (p *OllamaProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, 0, len(texts))
	for _, text := range texts {
		body := ollamaEmbedRequest{
			Model:  p.model,
			Prompt: text,
		}
		resp, err := p.doOllama(ctx, "POST", "/api/embeddings", body)
		if err != nil {
			return nil, err
		}

		var er ollamaEmbedResponse
		if err := json.NewDecoder(resp).Decode(&er); err != nil {
			return nil, fmt.Errorf("decode ollama embed response: %w", err)
		}
		result = append(result, er.Embedding)
	}
	return result, nil
}

// Models returns the list of models currently pulled on the Ollama server.
func (p *OllamaProvider) Models(ctx context.Context) ([]ModelInfo, error) {
	resp, err := p.doOllama(ctx, "GET", "/api/tags", nil)
	if err != nil {
		return nil, err
	}

	var tr ollamaTagsResponse
	if err := json.NewDecoder(resp).Decode(&tr); err != nil {
		return nil, fmt.Errorf("decode ollama tags response: %w", err)
	}

	models := make([]ModelInfo, len(tr.Models))
	for i, m := range tr.Models {
		models[i] = ModelInfo{ID: m.Name, Name: m.Name}
	}
	return models, nil
}

// Health performs a connectivity check against the Ollama server.
func (p *OllamaProvider) Health(ctx context.Context) error {
	_, err := p.doOllama(ctx, "GET", "/api/tags", nil)
	return err
}

func (p *OllamaProvider) effectiveBaseURL() string {
	if p.baseURL != "" {
		return p.baseURL
	}
	return "http://localhost:11434"
}

func (p *OllamaProvider) doOllama(ctx context.Context, method, path string, body interface{}) (io.ReadCloser, error) {
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

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama http %d for %s: %s", resp.StatusCode, path, sanitiseBody(bodyBytes))
	}

	return resp.Body, nil
}
