package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type OpenAICompatibleConfig struct {
	APIKey       string
	BaseURL      string
	DefaultModel string
	MaxTokens    int
	HTTPClient   *http.Client
	ProviderName string
}

type OpenAICompatibleProvider struct {
	apiKey       string
	baseURL      string
	defaultModel string
	maxTokens    int
	httpClient   *http.Client
	providerName string
}

func NewOpenAICompatibleProvider(config OpenAICompatibleConfig) (*OpenAICompatibleProvider, error) {
	baseURL, err := normalizeBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if config.MaxTokens <= 0 {
		config.MaxTokens = 2048
	}
	return &OpenAICompatibleProvider{
		apiKey: config.APIKey, baseURL: baseURL, defaultModel: config.DefaultModel,
		maxTokens: config.MaxTokens, httpClient: config.HTTPClient,
		providerName: strings.TrimSpace(config.ProviderName),
	}, nil
}

func NewDeepSeekProvider(apiKey, baseURL, model string, client *http.Client) (*OpenAICompatibleProvider, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.deepseek.com"
	}
	return NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		APIKey: apiKey, BaseURL: baseURL, DefaultModel: model, HTTPClient: client,
		ProviderName: "deepseek",
	})
}

func (provider *OpenAICompatibleProvider) Name() string {
	if provider != nil && provider.providerName != "" {
		return provider.providerName
	}
	return "openai-compatible"
}
func (*OpenAICompatibleProvider) String() string {
	return "OpenAICompatibleProvider{api_key:[REDACTED]}"
}
func (provider *OpenAICompatibleProvider) GoString() string { return provider.String() }

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model          string            `json:"model"`
	Messages       []openAIMessage   `json:"messages"`
	MaxTokens      int               `json:"max_tokens"`
	ResponseFormat map[string]string `json:"response_format,omitempty"`
}

type openAIResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (provider *OpenAICompatibleProvider) Text(
	ctx context.Context,
	request TextRequest,
) (Completion, error) {
	return provider.chat(ctx, request.System, request.User, request.Model, nil)
}

func (provider *OpenAICompatibleProvider) JSON(
	ctx context.Context,
	request JSONRequest,
) (Completion, error) {
	if len(request.Schema) == 0 || !json.Valid(request.Schema) {
		return Completion{}, errors.New("structured LLM schema must be valid JSON")
	}
	system := request.System + "\n\n你必须只返回一个 JSON object，不要 Markdown，不要解释。JSON 必须符合这个 schema：" + string(request.Schema)
	completion, err := provider.chat(ctx, system, request.User, request.Model,
		map[string]string{"type": "json_object"})
	if err != nil {
		return Completion{}, err
	}
	if !json.Valid([]byte(completion.Text)) {
		return Completion{}, &ProviderError{
			Provider: provider.Name(), Operation: "decode-json", Transient: true,
			Cause: ErrInvalidResponse,
		}
	}
	completion.JSON = json.RawMessage(completion.Text)
	completion.Text = ""
	return completion, nil
}

func (provider *OpenAICompatibleProvider) chat(
	ctx context.Context,
	system string,
	user string,
	model string,
	responseFormat map[string]string,
) (Completion, error) {
	if model == "" {
		model = provider.defaultModel
	}
	payload := openAIRequest{
		Model: model, MaxTokens: provider.maxTokens, ResponseFormat: responseFormat,
		Messages: []openAIMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
	}
	var response openAIResponse
	if err := postJSON(ctx, provider.httpClient, provider.Name(), provider.baseURL+"/chat/completions",
		map[string]string{"Authorization": "Bearer " + provider.apiKey}, payload, &response); err != nil {
		return Completion{}, err
	}
	if len(response.Choices) == 0 {
		return Completion{}, &ProviderError{
			Provider: provider.Name(), Operation: "decode", Transient: true, Cause: ErrInvalidResponse,
		}
	}
	return Completion{
		Text: response.Choices[0].Message.Content, Model: response.Model,
		Usage: Usage{InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens},
	}, nil
}
