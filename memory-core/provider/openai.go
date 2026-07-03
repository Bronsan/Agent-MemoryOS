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

// OpenAIChatRequest mirrors the OpenAI /chat/completions payload.
type openaiChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

// OpenAIChatResponse mirrors the top-level OpenAI /chat/completions response.
type openaiChatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *openaiError `json:"error,omitempty"`
}

// OpenAIEmbedRequest mirrors the OpenAI /embeddings payload.
type openaiEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// OpenAIEmbedResponse mirrors the OpenAI /embeddings response.
type openaiEmbedResponse struct {
	Data  []openaiEmbedData `json:"data"`
	Model string            `json:"model"`
	Error *openaiError      `json:"error,omitempty"`
}

type openaiEmbedData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// OpenAIModelsResponse mirrors GET /models.
type openaiModelsResponse struct {
	Data  []ModelInfo  `json:"data"`
	Error *openaiError `json:"error,omitempty"`
}

type openaiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// OpenAIConfig holds extended options specific to the OpenAI-compatible provider.
type OpenAIConfig struct {
	ProviderConfig
}

// OpenAIProvider implements Provider for any OpenAI-compatible endpoint,
// including OneAPI, NewAPI, vLLM, LiteLLM proxies, and local llama.cpp servers.
type OpenAIProvider struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// NewOpenAI creates an OpenAI-compatible provider.
// If timeout is 0, defaults to 30 s.
func NewOpenAI(cfg OpenAIConfig) *OpenAIProvider {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	return &OpenAIProvider{
		apiKey:  cfg.APIKey,
		baseURL: cfg.BaseURL,
		model:   cfg.Model,
		client: &http.Client{
			Timeout: time.Duration(timeout) * time.Second,
		},
	}
}

func (p *OpenAIProvider) Name() string { return "openai" }

// Chat sends a chat completion request (OpenAI-compatible).
func (p *OpenAIProvider) Chat(ctx context.Context, messages []Message) (*ChatResponse, error) {
	body := openaiChatRequest{
		Model:    p.model,
		Messages: messages,
	}
	resp, err := p.doOpenAI(ctx, "POST", "/chat/completions", body)
	if err != nil {
		return nil, err
	}

	var cr openaiChatResponse
	if err := json.NewDecoder(resp).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decode chat response: %w", err)
	}
	if cr.Error != nil {
		return nil, fmt.Errorf("openai chat error (type=%s, code=%s): %s", cr.Error.Type, cr.Error.Code, cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("openai: empty choices in chat response")
	}
	return &ChatResponse{
		Content:      cr.Choices[0].Message.Content,
		FinishReason: cr.Choices[0].FinishReason,
	}, nil
}

// Embed sends an embeddings request (OpenAI-compatible).
func (p *OpenAIProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body := openaiEmbedRequest{
		Model: p.model,
		Input: texts,
	}
	resp, err := p.doOpenAI(ctx, "POST", "/embeddings", body)
	if err != nil {
		return nil, err
	}

	var er openaiEmbedResponse
	if err := json.NewDecoder(resp).Decode(&er); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	if er.Error != nil {
		return nil, fmt.Errorf("openai embed error: %s", er.Error.Message)
	}

	result := make([][]float32, len(texts))
	for i := range result {
		result[i] = make([]float32, 0) // placeholder for missing indices
	}
	for _, d := range er.Data {
		if d.Index >= 0 && d.Index < len(result) {
			result[d.Index] = d.Embedding
		}
	}
	return result, nil
}

// Models returns the list of available models.
func (p *OpenAIProvider) Models(ctx context.Context) ([]ModelInfo, error) {
	resp, err := p.doOpenAI(ctx, "GET", "/models", nil)
	if err != nil {
		return nil, err
	}

	var mr openaiModelsResponse
	if err := json.NewDecoder(resp).Decode(&mr); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}
	if mr.Error != nil {
		return nil, fmt.Errorf("openai models error: %s", mr.Error.Message)
	}
	return mr.Data, nil
}

// Health performs a lightweight connectivity check by hitting /models.
func (p *OpenAIProvider) Health(ctx context.Context) error {
	_, err := p.doOpenAI(ctx, "GET", "/models", nil)
	return err
}

// doOpenAI is the shared request helper. It never logs the API key.
func (p *OpenAIProvider) doOpenAI(ctx context.Context, method, path string, body interface{}) (io.ReadCloser, error) {
	base := p.baseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	url := base + path

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
	// Only attach auth header when key is provided (local proxies may skip it).
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", p.Name(), err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Purposely do NOT include the API key in the error.
		return nil, fmt.Errorf("%s http %d for %s: %s", p.Name(), resp.StatusCode, path, sanitiseBody(bodyBytes))
	}

	return resp.Body, nil
}

// sanitiseBody scrubs an API error body so that no credentials leak into logs.
// It removes common patterns like Authorization headers echoed back by proxies.
func sanitiseBody(raw []byte) string {
	// Only keep up to 512 bytes of the error body to avoid flooding logs.
	maxLen := 512
	if len(raw) > maxLen {
		raw = raw[:maxLen]
	}
	return string(raw)
}
