package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type Budget struct {
	MaxCalls    int
	MaxTokens   int
	MaxCostUSD  float64
	MaxWallTime time.Duration
}

type CostEstimator func(model string, usage Usage) float64

type Options struct {
	Enabled          bool
	Model            string
	FastModel        string
	MaxRetries       int
	BaseDelay        time.Duration
	Timeout          time.Duration
	BreakerThreshold int
	BreakerCooldown  time.Duration
	Budget           Budget
	Metrics          *Metrics
	CostEstimator    CostEstimator
	UsageObserver    UsageObserver
	MaxOutputTokens  int
}

type budgetState struct {
	mu             sync.Mutex
	startedAt      time.Time
	generation     uint64
	calls          int
	tokens         int
	reservedTokens int
	costUSD        float64
}

type circuitState struct {
	mu          sync.Mutex
	failures    int
	openUntil   time.Time
	halfOpenRun bool
}

type Client struct {
	provider         Provider
	enabled          bool
	model            string
	fastModel        string
	maxRetries       int
	baseDelay        time.Duration
	timeout          time.Duration
	breakerThreshold int
	breakerCooldown  time.Duration
	budget           Budget
	budgetState      budgetState
	circuit          *circuitState
	metrics          *Metrics
	costEstimator    CostEstimator
	usageObserver    UsageObserver
	maxOutputTokens  int
	randMu           sync.Mutex
	rand             *rand.Rand
}

var usageCallSequence atomic.Uint64

// ResilientLLM is retained as a descriptive alias for callers migrating from
// the provider-independent contract.
type ResilientLLM = Client

func NewClient(provider Provider, options Options) *Client {
	if options.MaxRetries < 0 {
		options.MaxRetries = 0
	}
	if options.Timeout <= 0 {
		options.Timeout = 30 * time.Second
	}
	if options.BaseDelay < 0 {
		options.BaseDelay = 0
	}
	if options.BreakerThreshold <= 0 {
		options.BreakerThreshold = 4
	}
	if options.BreakerCooldown <= 0 {
		options.BreakerCooldown = 15 * time.Second
	}
	metrics := options.Metrics
	if metrics == nil {
		metrics = NewMetrics()
	}
	now := time.Now()
	return &Client{
		provider: provider, enabled: options.Enabled && provider != nil,
		model: options.Model, fastModel: options.FastModel,
		maxRetries: options.MaxRetries, baseDelay: options.BaseDelay, timeout: options.Timeout,
		breakerThreshold: options.BreakerThreshold, breakerCooldown: options.BreakerCooldown,
		budget: options.Budget, budgetState: budgetState{startedAt: now}, metrics: metrics,
		costEstimator:   options.CostEstimator,
		usageObserver:   options.UsageObserver,
		maxOutputTokens: max(0, options.MaxOutputTokens),
		circuit:         &circuitState{},
		rand:            rand.New(rand.NewSource(now.UnixNano())),
	}
}

// NewSession creates independent run-scoped budget accounting while sharing
// the provider, redacted metrics, and circuit breaker. This is the preferred
// way to enforce BudgetConfig for concurrent orchestrator runs.
func (client *Client) NewSession() *Client {
	if client == nil {
		return nil
	}
	now := time.Now()
	return &Client{
		provider: client.provider, enabled: client.enabled,
		model: client.model, fastModel: client.fastModel,
		maxRetries: client.maxRetries, baseDelay: client.baseDelay, timeout: client.timeout,
		breakerThreshold: client.breakerThreshold, breakerCooldown: client.breakerCooldown,
		budget: client.budget, budgetState: budgetState{startedAt: now},
		circuit: client.circuit, metrics: client.metrics, costEstimator: client.costEstimator,
		usageObserver:   client.usageObserver,
		maxOutputTokens: client.maxOutputTokens,
		rand:            rand.New(rand.NewSource(now.UnixNano())),
	}
}

func (client *Client) Enabled() bool { return client != nil && client.enabled }

func (client *Client) Metrics() MetricsSnapshot {
	if client == nil {
		return MetricsSnapshot{}
	}
	return client.metrics.Snapshot()
}

func (client *Client) Text(
	ctx context.Context,
	system string,
	user string,
	fast bool,
) (string, error) {
	model := client.selectedModel(fast)
	completion, err := client.call(ctx, model, "text", system, user, func(attempt context.Context) (Completion, error) {
		return client.provider.Text(attempt, TextRequest{System: system, User: user, Model: model})
	})
	if err != nil {
		return "", err
	}
	return completion.Text, nil
}

func (client *Client) JSON(
	ctx context.Context,
	system string,
	user string,
	schema json.RawMessage,
	out any,
	fast bool,
) error {
	if out == nil {
		return errors.New("llm JSON output target must not be nil")
	}
	model := client.selectedModel(fast)
	completion, err := client.call(ctx, model, "json", system, user, func(attempt context.Context) (Completion, error) {
		return client.provider.JSON(attempt, JSONRequest{
			System: system, User: user, Model: model, Schema: append(json.RawMessage(nil), schema...),
		})
	})
	if err != nil {
		return err
	}
	if len(completion.JSON) == 0 || !json.Valid(completion.JSON) {
		client.metrics.update(func(snapshot *MetricsSnapshot) { snapshot.ParseErrors++ })
		return ErrInvalidResponse
	}
	if err := json.Unmarshal(completion.JSON, out); err != nil {
		client.metrics.update(func(snapshot *MetricsSnapshot) { snapshot.ParseErrors++ })
		return ErrInvalidResponse
	}
	return nil
}

func (client *Client) selectedModel(fast bool) string {
	if client == nil {
		return ""
	}
	if fast && client.fastModel != "" {
		return client.fastModel
	}
	return client.model
}

type providerCall func(context.Context) (Completion, error)

func (client *Client) call(
	ctx context.Context,
	model string,
	operation string,
	system string,
	user string,
	invoke providerCall,
) (Completion, error) {
	if client == nil || !client.enabled || client.provider == nil {
		return Completion{}, ErrDisabled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Completion{}, err
	}
	if !client.beforeCircuit(time.Now()) {
		client.metrics.update(func(snapshot *MetricsSnapshot) { snapshot.ShortCircuits++ })
		return Completion{}, ErrCircuitOpen
	}
	estimatedInput := estimateTokens(system) + estimateTokens(user)
	metadata := usageMetadata(ctx)
	event := UsageEvent{
		ID: strconv.FormatInt(time.Now().UTC().UnixNano(), 36) + "-" + strconv.FormatUint(usageCallSequence.Add(1), 36),
		TS: time.Now().UTC(), RunID: metadata.RunID, Symbol: metadata.Symbol,
		Agent: metadata.Agent, Source: metadata.Source, Provider: client.provider.Name(),
		Model: model, Operation: operation, InputTokens: estimatedInput, TokenEstimated: true,
	}
	startedAt := time.Now()
	if client.usageObserver != nil {
		// Reserve the provider's maximum response allowance as well as the
		// prompt. Concurrent calls near the daily ceiling can otherwise all
		// pass the gate and overshoot it with their completions.
		event.OutputTokens = client.maxOutputTokens
		if client.costEstimator != nil {
			event.CostUSD = client.costEstimator(model, Usage{
				InputTokens: event.InputTokens, OutputTokens: event.OutputTokens,
			})
			event.CostEstimated = event.CostUSD > 0
		}
		if err := client.usageObserver.Reserve(ctx, event); err != nil {
			event.Status, event.ErrorKind = "budget_rejected", "daily_budget"
			event.InputTokens, event.OutputTokens = 0, 0
			event.CostUSD, event.CostEstimated = 0, false
			client.usageObserver.Record(ctx, event)
			client.metrics.update(func(snapshot *MetricsSnapshot) { snapshot.BudgetRejections++ })
			client.releaseHalfOpen()
			return Completion{}, err
		}
		event.OutputTokens = 0
		event.CostUSD, event.CostEstimated = 0, false
	}
	reservation, err := client.reserveBudget(estimatedInput)
	if err != nil {
		event.Status, event.ErrorKind = "budget_rejected", "run_budget"
		event.InputTokens = 0
		client.recordUsage(ctx, event, startedAt)
		client.releaseHalfOpen()
		client.metrics.update(func(snapshot *MetricsSnapshot) { snapshot.BudgetRejections++ })
		return Completion{}, err
	}
	client.metrics.update(func(snapshot *MetricsSnapshot) { snapshot.Calls++ })

	for attempt := 0; attempt <= client.maxRetries; attempt++ {
		attemptContext := ctx
		cancel := func() {}
		if client.timeout > 0 {
			attemptContext, cancel = context.WithTimeout(ctx, client.timeout)
		}
		completion, callErr := invoke(attemptContext)
		cancel()
		if callErr == nil {
			client.recordCircuitSuccess()
			client.completeUsage(&completion, model, estimatedInput)
			event.Model = completion.Model
			event.InputTokens, event.OutputTokens, event.CostUSD = completion.Usage.InputTokens, completion.Usage.OutputTokens, completion.Usage.CostUSD
			event.TokenEstimated, event.CostEstimated = completion.Usage.TokenEstimated, completion.Usage.CostEstimated
			client.metrics.update(func(snapshot *MetricsSnapshot) {
				snapshot.InputTokens += uint64(completion.Usage.InputTokens)
				snapshot.OutputTokens += uint64(completion.Usage.OutputTokens)
				snapshot.CostUSD += completion.Usage.CostUSD
			})
			if err := client.settleBudget(reservation, completion.Usage); err != nil {
				event.Status, event.ErrorKind = "budget_rejected", "run_budget_after_response"
				client.recordUsage(ctx, event, startedAt)
				client.metrics.update(func(snapshot *MetricsSnapshot) { snapshot.BudgetRejections++ })
				return Completion{}, err
			}
			client.metrics.update(func(snapshot *MetricsSnapshot) {
				snapshot.Successes++
			})
			event.Status = "success"
			event.TokenEstimated, event.CostEstimated = completion.Usage.TokenEstimated, completion.Usage.CostEstimated
			client.recordUsage(ctx, event, startedAt)
			return completion, nil
		}

		client.recordCircuitFailure(time.Now())
		client.metrics.update(func(snapshot *MetricsSnapshot) {
			snapshot.Errors++
			if errors.Is(callErr, context.DeadlineExceeded) {
				snapshot.Timeouts++
			}
		})
		if ctx.Err() != nil {
			client.settleFailedBudget(reservation)
			event.Status, event.ErrorKind = "error", "canceled"
			client.recordUsage(ctx, event, startedAt)
			return Completion{}, ctx.Err()
		}
		transient := isTransient(callErr) || errors.Is(callErr, context.DeadlineExceeded)
		if !transient || attempt >= client.maxRetries {
			client.settleFailedBudget(reservation)
			event.Status, event.ErrorKind = "error", usageErrorKind(callErr)
			client.recordUsage(ctx, event, startedAt)
			return Completion{}, callErr
		}
		client.metrics.update(func(snapshot *MetricsSnapshot) { snapshot.Retries++ })
		if err := sleepContext(ctx, client.retryDelay(attempt)); err != nil {
			client.settleFailedBudget(reservation)
			event.Status, event.ErrorKind = "error", "canceled"
			client.recordUsage(ctx, event, startedAt)
			return Completion{}, err
		}
	}
	client.settleFailedBudget(reservation)
	event.Status, event.ErrorKind = "error", "invalid_response"
	client.recordUsage(ctx, event, startedAt)
	return Completion{}, ErrInvalidResponse
}

func (client *Client) recordUsage(ctx context.Context, event UsageEvent, startedAt time.Time) {
	if client == nil || client.usageObserver == nil {
		return
	}
	event.DurationMS = time.Since(startedAt).Milliseconds()
	client.usageObserver.Record(ctx, event)
}

func usageErrorKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrInvalidResponse):
		return "invalid_response"
	default:
		return "provider_error"
	}
}

func (client *Client) completeUsage(completion *Completion, model string, estimatedInput int) {
	if completion.Model == "" {
		completion.Model = model
	}
	if completion.Usage.InputTokens <= 0 {
		completion.Usage.InputTokens = estimatedInput
		completion.Usage.TokenEstimated = true
	}
	if completion.Usage.OutputTokens <= 0 {
		if len(completion.JSON) > 0 {
			completion.Usage.OutputTokens = estimateTokens(string(completion.JSON))
		} else {
			completion.Usage.OutputTokens = estimateTokens(completion.Text)
		}
		completion.Usage.TokenEstimated = true
	}
	completion.Usage.InputTokens = max(0, completion.Usage.InputTokens)
	completion.Usage.OutputTokens = max(0, completion.Usage.OutputTokens)
	if math.IsNaN(completion.Usage.CostUSD) || math.IsInf(completion.Usage.CostUSD, 0) || completion.Usage.CostUSD < 0 {
		completion.Usage.CostUSD = 0
	}
	if completion.Usage.CostUSD <= 0 && client.costEstimator != nil {
		estimated := client.costEstimator(completion.Model, completion.Usage)
		if !math.IsNaN(estimated) && !math.IsInf(estimated, 0) && estimated > 0 {
			completion.Usage.CostUSD = estimated
			completion.Usage.CostEstimated = true
		}
	}
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	// A conservative provider-independent estimate used only when an API omits
	// usage. It never records prompt text.
	return (len([]byte(text)) + 3) / 4
}

func (client *Client) retryDelay(attempt int) time.Duration {
	if client.baseDelay <= 0 {
		return 0
	}
	delay := client.baseDelay << attempt
	client.randMu.Lock()
	jitter := time.Duration(client.rand.Int63n(int64(client.baseDelay) + 1))
	client.randMu.Unlock()
	return delay + jitter
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (client *Client) beforeCircuit(now time.Time) bool {
	client.circuit.mu.Lock()
	defer client.circuit.mu.Unlock()
	if client.circuit.failures < client.breakerThreshold {
		return true
	}
	if now.Before(client.circuit.openUntil) {
		return false
	}
	if client.circuit.halfOpenRun {
		return false
	}
	client.circuit.halfOpenRun = true
	return true
}

func (client *Client) releaseHalfOpen() {
	client.circuit.mu.Lock()
	client.circuit.halfOpenRun = false
	client.circuit.mu.Unlock()
}

func (client *Client) recordCircuitSuccess() {
	client.circuit.mu.Lock()
	client.circuit.failures = 0
	client.circuit.openUntil = time.Time{}
	client.circuit.halfOpenRun = false
	client.circuit.mu.Unlock()
}

func (client *Client) recordCircuitFailure(now time.Time) {
	client.circuit.mu.Lock()
	client.circuit.failures++
	client.circuit.halfOpenRun = false
	if client.circuit.failures >= client.breakerThreshold {
		client.circuit.openUntil = now.Add(client.breakerCooldown)
	}
	client.circuit.mu.Unlock()
}

type budgetReservation struct {
	tokens     int
	generation uint64
}

func (client *Client) reserveBudget(estimatedTokens int) (budgetReservation, error) {
	client.budgetState.mu.Lock()
	defer client.budgetState.mu.Unlock()
	state := &client.budgetState
	if client.budget.MaxWallTime > 0 && time.Since(state.startedAt) >= client.budget.MaxWallTime {
		// Treat MaxWallTime as the budget window. This prevents a long-lived
		// server from becoming permanently disabled after its first run.
		state.startedAt = time.Now()
		state.calls, state.tokens, state.reservedTokens, state.costUSD = 0, 0, 0, 0
		state.generation++
	}
	if client.budget.MaxCalls > 0 && state.calls >= client.budget.MaxCalls {
		return budgetReservation{}, ErrBudgetExceeded
	}
	if client.budget.MaxTokens > 0 && state.tokens+state.reservedTokens+estimatedTokens > client.budget.MaxTokens {
		return budgetReservation{}, ErrBudgetExceeded
	}
	if client.budget.MaxCostUSD > 0 && state.costUSD >= client.budget.MaxCostUSD {
		return budgetReservation{}, ErrBudgetExceeded
	}
	state.calls++
	state.reservedTokens += estimatedTokens
	return budgetReservation{tokens: estimatedTokens, generation: state.generation}, nil
}

// ResetBudget starts a fresh budget window. Orchestrators may call this when
// constructing an isolated run-scoped LLM session; it is concurrency-safe.
func (client *Client) ResetBudget() {
	if client == nil {
		return
	}
	client.budgetState.mu.Lock()
	client.budgetState.startedAt = time.Now()
	client.budgetState.generation++
	client.budgetState.calls = 0
	client.budgetState.tokens = 0
	client.budgetState.reservedTokens = 0
	client.budgetState.costUSD = 0
	client.budgetState.mu.Unlock()
}

func (client *Client) settleBudget(reservation budgetReservation, usage Usage) error {
	client.budgetState.mu.Lock()
	defer client.budgetState.mu.Unlock()
	state := &client.budgetState
	if reservation.generation != state.generation {
		return nil
	}
	state.reservedTokens -= reservation.tokens
	state.tokens += max(0, usage.TotalTokens())
	state.costUSD += maxFloat(0, usage.CostUSD)
	if client.budget.MaxTokens > 0 && state.tokens > client.budget.MaxTokens {
		return ErrBudgetExceeded
	}
	if client.budget.MaxCostUSD > 0 && state.costUSD > client.budget.MaxCostUSD {
		return ErrBudgetExceeded
	}
	return nil
}

func (client *Client) settleFailedBudget(reservation budgetReservation) {
	client.budgetState.mu.Lock()
	if reservation.generation != client.budgetState.generation {
		client.budgetState.mu.Unlock()
		return
	}
	client.budgetState.reservedTokens -= reservation.tokens
	// A failed remote request may still have consumed the prompt.
	client.budgetState.tokens += reservation.tokens
	client.budgetState.mu.Unlock()
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}
