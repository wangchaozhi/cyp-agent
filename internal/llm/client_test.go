package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/config"
)

func testOptions() Options {
	return Options{
		Enabled: true, Model: "model", FastModel: "fast",
		MaxRetries: 2, Timeout: time.Second,
		BreakerThreshold: 10, BreakerCooldown: time.Second,
	}
}

func TestClientRetriesOnlyTransientErrors(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider(2, true)
	client := NewClient(provider, testOptions())
	text, err := client.Text(context.Background(), "system", "user", false)
	if err != nil || !strings.Contains(text, "mock") {
		t.Fatalf("Text = %q, %v", text, err)
	}
	metrics := client.Metrics()
	if provider.Calls() != 3 || metrics.Calls != 1 || metrics.Errors != 2 || metrics.Retries != 2 || metrics.Successes != 1 {
		t.Fatalf("provider calls=%d metrics=%+v", provider.Calls(), metrics)
	}

	fatal := NewMockProvider(3, false)
	fatalClient := NewClient(fatal, testOptions())
	if _, err := fatalClient.Text(context.Background(), "s", "u", false); err == nil {
		t.Fatal("permanent provider error unexpectedly succeeded")
	}
	if fatal.Calls() != 1 || fatalClient.Metrics().Retries != 0 {
		t.Fatalf("permanent failure was retried: calls=%d metrics=%+v", fatal.Calls(), fatalClient.Metrics())
	}
}

func TestClientCircuitBreakerAndHalfOpenRecovery(t *testing.T) {
	provider := NewMockProvider(2, false)
	options := testOptions()
	options.MaxRetries = 0
	options.BreakerThreshold = 2
	options.BreakerCooldown = 10 * time.Millisecond
	client := NewClient(provider, options)
	for index := 0; index < 2; index++ {
		if _, err := client.Text(context.Background(), "s", "u", false); err == nil {
			t.Fatal("expected provider failure")
		}
	}
	if _, err := client.Text(context.Background(), "s", "u", false); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("circuit error = %v", err)
	}
	time.Sleep(15 * time.Millisecond)
	if _, err := client.Text(context.Background(), "s", "u", false); err != nil {
		t.Fatalf("half-open probe did not recover: %v", err)
	}
	if client.Metrics().ShortCircuits != 1 {
		t.Fatalf("metrics = %+v", client.Metrics())
	}
}

func TestClientAttemptDeadlineIsFinite(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider(0, true)
	provider.TextFunc = func(ctx context.Context, _ TextRequest) (Completion, error) {
		<-ctx.Done()
		return Completion{}, ctx.Err()
	}
	options := testOptions()
	options.Timeout = 5 * time.Millisecond
	options.MaxRetries = 1
	client := NewClient(provider, options)
	started := time.Now()
	_, err := client.Text(context.Background(), "s", "u", false)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("deadline result err=%v duration=%s", err, time.Since(started))
	}
	if client.Metrics().Timeouts != 2 || client.Metrics().Retries != 1 {
		t.Fatalf("metrics = %+v", client.Metrics())
	}
}

func TestClientBudgetAndReset(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider(0, true)
	provider.TextFunc = func(_ context.Context, request TextRequest) (Completion, error) {
		return Completion{Text: "ok", Model: request.Model, Usage: Usage{InputTokens: 2, OutputTokens: 2, CostUSD: 0.25}}, nil
	}
	options := testOptions()
	options.Budget = Budget{MaxCalls: 1, MaxTokens: 100, MaxCostUSD: 1}
	client := NewClient(provider, options)
	if _, err := client.Text(context.Background(), "s", "u", false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Text(context.Background(), "s", "u", false); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("budget error = %v", err)
	}
	client.ResetBudget()
	if _, err := client.Text(context.Background(), "s", "u", false); err != nil {
		t.Fatalf("fresh budget window failed: %v", err)
	}

	tokenOptions := testOptions()
	tokenOptions.Budget = Budget{MaxTokens: 3}
	tokenClient := NewClient(provider, tokenOptions)
	if _, err := tokenClient.Text(context.Background(), "s", "u", false); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("post-response token budget error = %v", err)
	}
	if tokenClient.Metrics().InputTokens != 2 || tokenClient.Metrics().OutputTokens != 2 {
		t.Fatalf("consumed usage missing after rejection: %+v", tokenClient.Metrics())
	}
}

func TestRunSessionsHaveIndependentBudgetsAndSharedCircuit(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider(0, true)
	options := testOptions()
	options.Budget = Budget{MaxCalls: 1, MaxTokens: 100}
	base := NewClient(provider, options)
	first, second := base.NewSession(), base.NewSession()
	if _, err := first.Text(context.Background(), "s", "u", false); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Text(context.Background(), "s", "u", false); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Text(context.Background(), "s", "u", false); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("first session budget = %v", err)
	}
	if base.Metrics().Calls != 2 {
		t.Fatalf("sessions did not share metrics: %+v", base.Metrics())
	}

	failing := NewMockProvider(1, false)
	options = testOptions()
	options.MaxRetries = 0
	options.BreakerThreshold = 1
	sharedBase := NewClient(failing, options)
	if _, err := sharedBase.NewSession().Text(context.Background(), "s", "u", false); err == nil {
		t.Fatal("expected provider failure")
	}
	if _, err := sharedBase.NewSession().Text(context.Background(), "s", "u", false); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("session did not share circuit: %v", err)
	}
}

func TestClientStructuredOutputValidation(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider(0, true)
	provider.JSONFunc = func(context.Context, JSONRequest) (Completion, error) {
		return Completion{JSON: json.RawMessage(`{"risk_score":0.8}`)}, nil
	}
	client := NewClient(provider, testOptions())
	var result struct {
		RiskScore float64 `json:"risk_score"`
	}
	if err := client.JSON(context.Background(), "s", "u", json.RawMessage(`{"type":"object"}`), &result, false); err != nil {
		t.Fatal(err)
	}
	if result.RiskScore != 0.8 {
		t.Fatalf("result = %+v", result)
	}
	provider.JSONFunc = func(context.Context, JSONRequest) (Completion, error) {
		return Completion{JSON: json.RawMessage(`{"risk_score":`)}, nil
	}
	if err := client.JSON(context.Background(), "s", "u", json.RawMessage(`{}`), &result, false); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("invalid JSON error = %v", err)
	}
	if client.Metrics().ParseErrors != 1 {
		t.Fatalf("metrics = %+v", client.Metrics())
	}
}

func TestClientMetricsAreConcurrentAndRedacted(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider(0, true)
	client := NewClient(provider, testOptions())
	var wait sync.WaitGroup
	for index := 0; index < 50; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _ = client.Text(context.Background(), "api_key=super-secret", "private prompt", false)
		}()
	}
	wait.Wait()
	if client.Metrics().Calls != 50 || client.Metrics().Successes != 50 {
		t.Fatalf("metrics = %+v", client.Metrics())
	}
	encoded, err := json.Marshal(client.Metrics())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "super-secret") || strings.Contains(string(encoded), "private prompt") {
		t.Fatalf("metrics leaked request content: %s", encoded)
	}
}

func TestFromSettingsDisablesMissingKeyAndUsesFiniteCostEstimate(t *testing.T) {
	t.Parallel()
	settings := config.DefaultSettings()
	client := FromSettings(settings)
	if client.Enabled() {
		t.Fatal("missing key unexpectedly enabled LLM")
	}
	if _, err := client.Text(context.Background(), "s", "u", false); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled error = %v", err)
	}
	cost := ConservativeCostEstimator("any", Usage{InputTokens: 1000, OutputTokens: 1000})
	if cost <= 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		t.Fatalf("cost = %v", cost)
	}
}
