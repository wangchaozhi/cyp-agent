package llm

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

type MockProvider struct {
	TextFunc func(context.Context, TextRequest) (Completion, error)
	JSONFunc func(context.Context, JSONRequest) (Completion, error)

	mu                 sync.Mutex
	failuresLeft       int
	failureIsTransient bool
	calls              int
}

func NewMockProvider(failures int, transient bool) *MockProvider {
	if failures < 0 {
		failures = 0
	}
	return &MockProvider{failuresLeft: failures, failureIsTransient: transient}
}

func (*MockProvider) Name() string { return "mock" }

func (provider *MockProvider) Calls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

func (provider *MockProvider) maybeFail(operation string) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls++
	if provider.failuresLeft <= 0 {
		return nil
	}
	provider.failuresLeft--
	return &ProviderError{Provider: "mock", Operation: operation, Transient: provider.failureIsTransient}
}

func (provider *MockProvider) Text(ctx context.Context, request TextRequest) (Completion, error) {
	if err := ctx.Err(); err != nil {
		return Completion{}, err
	}
	if err := provider.maybeFail("text"); err != nil {
		return Completion{}, err
	}
	if provider.TextFunc != nil {
		return provider.TextFunc(ctx, request)
	}
	return Completion{Text: "[mock] 无 LLM 密钥，返回占位文本。", Model: request.Model}, nil
}

func (provider *MockProvider) JSON(ctx context.Context, request JSONRequest) (Completion, error) {
	if err := ctx.Err(); err != nil {
		return Completion{}, err
	}
	if err := provider.maybeFail("json"); err != nil {
		return Completion{}, err
	}
	if provider.JSONFunc != nil {
		return provider.JSONFunc(ctx, request)
	}
	return Completion{}, &ProviderError{
		Provider: "mock", Operation: "json", Transient: true,
		Cause: errors.New("mock has no structured output"),
	}
}

func JSONCompletion(value any) (Completion, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return Completion{}, err
	}
	return Completion{JSON: encoded}, nil
}
