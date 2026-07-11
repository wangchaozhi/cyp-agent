package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type AnthropicConfig struct {
	APIKey       string
	BaseURL      string
	DefaultModel string
	MaxTokens    int
	HTTPClient   *http.Client
}

type AnthropicProvider struct {
	apiKey       string
	baseURL      string
	defaultModel string
	maxTokens    int
	httpClient   *http.Client
}

func NewAnthropicProvider(config AnthropicConfig) (*AnthropicProvider, error) {
	if strings.TrimSpace(config.BaseURL) == "" {
		config.BaseURL = "https://api.anthropic.com"
	}
	baseURL, err := normalizeBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if config.MaxTokens <= 0 {
		config.MaxTokens = 2048
	}
	return &AnthropicProvider{
		apiKey: config.APIKey, baseURL: baseURL, defaultModel: config.DefaultModel,
		maxTokens: config.MaxTokens, httpClient: config.HTTPClient,
	}, nil
}

func (*AnthropicProvider) Name() string              { return "anthropic" }
func (*AnthropicProvider) String() string            { return "AnthropicProvider{api_key:[REDACTED]}" }
func (provider *AnthropicProvider) GoString() string { return provider.String() }

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicRequest struct {
	Model      string             `json:"model"`
	MaxTokens  int                `json:"max_tokens"`
	System     string             `json:"system"`
	Messages   []anthropicMessage `json:"messages"`
	Tools      []anthropicTool    `json:"tools,omitempty"`
	ToolChoice map[string]string  `json:"tool_choice,omitempty"`
}

type anthropicResponse struct {
	Model   string `json:"model"`
	Content []struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Text  string          `json:"text"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (provider *AnthropicProvider) Text(
	ctx context.Context,
	request TextRequest,
) (Completion, error) {
	response, err := provider.messages(ctx, anthropicRequest{
		Model: request.Model, MaxTokens: provider.maxTokens, System: request.System,
		Messages: []anthropicMessage{{Role: "user", Content: request.User}},
	})
	if err != nil {
		return Completion{}, err
	}
	var text strings.Builder
	for _, block := range response.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return Completion{
		Text: text.String(), Model: response.Model,
		Usage: Usage{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens},
	}, nil
}

func (provider *AnthropicProvider) JSON(
	ctx context.Context,
	request JSONRequest,
) (Completion, error) {
	if len(request.Schema) == 0 || !json.Valid(request.Schema) {
		return Completion{}, errors.New("structured LLM schema must be valid JSON")
	}
	response, err := provider.messages(ctx, anthropicRequest{
		Model: request.Model, MaxTokens: provider.maxTokens, System: request.System,
		Messages:   []anthropicMessage{{Role: "user", Content: request.User}},
		Tools:      []anthropicTool{{Name: "emit", Description: "以给定 schema 返回结构化结果", InputSchema: request.Schema}},
		ToolChoice: map[string]string{"type": "tool", "name": "emit"},
	})
	if err != nil {
		return Completion{}, err
	}
	for _, block := range response.Content {
		if block.Type == "tool_use" && block.Name == "emit" && json.Valid(block.Input) {
			return Completion{
				JSON: append(json.RawMessage(nil), block.Input...), Model: response.Model,
				Usage: Usage{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens},
			}, nil
		}
	}
	return Completion{}, &ProviderError{
		Provider: provider.Name(), Operation: "decode-tool", Transient: true, Cause: ErrInvalidResponse,
	}
}

func (provider *AnthropicProvider) messages(
	ctx context.Context,
	payload anthropicRequest,
) (anthropicResponse, error) {
	if payload.Model == "" {
		payload.Model = provider.defaultModel
	}
	var response anthropicResponse
	err := postJSON(ctx, provider.httpClient, provider.Name(), provider.baseURL+"/v1/messages",
		map[string]string{"x-api-key": provider.apiKey, "anthropic-version": "2023-06-01"},
		payload, &response)
	return response, err
}
