package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAICompatibleTextAndStructuredJSON(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("authorization header was not set")
		}
		var payload openAIRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		content := "hello"
		if payload.ResponseFormat["type"] == "json_object" {
			content = `{"risk_score":0.7}`
			if !strings.Contains(payload.Messages[0].Content, "JSON 必须符合") {
				t.Error("schema instruction missing")
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "returned-model",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 3},
		})
	}))
	defer server.Close()
	provider, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		APIKey: "test-secret", BaseURL: server.URL, DefaultModel: "default", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	text, err := provider.Text(context.Background(), TextRequest{System: "system", User: "user"})
	if err != nil || text.Text != "hello" || text.Usage.InputTokens != 11 {
		t.Fatalf("text = %+v, %v", text, err)
	}
	structured, err := provider.JSON(context.Background(), JSONRequest{
		System: "system", User: "user", Schema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil || string(structured.JSON) != `{"risk_score":0.7}` {
		t.Fatalf("JSON = %+v, %v", structured, err)
	}
	if strings.Contains(fmt.Sprintf("%#v", provider), "test-secret") {
		t.Fatal("provider representation leaked API key")
	}
}

func TestAnthropicTextAndToolUseJSON(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/messages" || request.Header.Get("x-api-key") != "anthropic-secret" {
			t.Errorf("request path/header mismatch")
		}
		var payload anthropicRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		content := []any{map[string]any{"type": "text", "text": "hello"}}
		if len(payload.Tools) > 0 {
			content = []any{map[string]any{"type": "tool_use", "name": "emit", "input": map[string]any{"risk_score": 0.9}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "claude-test", "content": content,
			"usage": map[string]any{"input_tokens": 5, "output_tokens": 2},
		})
	}))
	defer server.Close()
	provider, err := NewAnthropicProvider(AnthropicConfig{
		APIKey: "anthropic-secret", BaseURL: server.URL, DefaultModel: "claude", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	text, err := provider.Text(context.Background(), TextRequest{System: "s", User: "u"})
	if err != nil || text.Text != "hello" {
		t.Fatalf("text = %+v, %v", text, err)
	}
	structured, err := provider.JSON(context.Background(), JSONRequest{
		System: "s", User: "u", Schema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil || string(structured.JSON) != `{"risk_score":0.9}` {
		t.Fatalf("structured = %+v, %v", structured, err)
	}
	if strings.Contains(fmt.Sprintf("%#v", provider), "anthropic-secret") {
		t.Fatal("provider representation leaked API key")
	}
}

func TestHTTPProviderClassifiesStatusWithoutLeakingBody(t *testing.T) {
	t.Parallel()
	secretBody := "provider-body-super-secret"
	var status atomic.Int32
	status.Store(http.StatusTooManyRequests)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(secretBody))
	}))
	defer server.Close()
	provider, err := NewDeepSeekProvider("key", server.URL, "model", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Text(context.Background(), TextRequest{System: "s", User: "u"})
	if err == nil || !isTransient(err) || strings.Contains(err.Error(), secretBody) {
		t.Fatalf("429 error = %v", err)
	}
	status.Store(http.StatusBadRequest)
	_, err = provider.Text(context.Background(), TextRequest{System: "s", User: "u"})
	if err == nil || isTransient(err) || strings.Contains(err.Error(), secretBody) {
		t.Fatalf("400 error = %v", err)
	}
}

func TestHTTPProviderHonorsContextDeadline(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
			w.WriteHeader(http.StatusGatewayTimeout)
		}
	}))
	defer server.Close()
	provider, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		APIKey: "key", BaseURL: server.URL, DefaultModel: "model", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = provider.Text(ctx, TextRequest{System: "s", User: "u"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
}

func TestProviderRejectsCredentialBearingBaseURL(t *testing.T) {
	t.Parallel()
	if _, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		BaseURL: "https://user:password@example.com", DefaultModel: "model",
	}); err == nil {
		t.Fatal("credential-bearing base URL unexpectedly accepted")
	}
}
