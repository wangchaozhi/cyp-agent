package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestSafetyStateRequiresSuccessfulReconcileAndRejectsLive(t *testing.T) {
	t.Parallel()
	safety := NewSafetyState()
	paper := RuntimeState{Mode: "paper", ExecutionVenue: "paper"}
	if err := safety.CheckNewPosition(paper); !errors.Is(err, ErrReconciliationFrozen) {
		t.Fatalf("initial guard = %v", err)
	}
	if err := safety.CompleteReconcile(ReconcileReport{OK: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := safety.CheckNewPosition(paper); err != nil {
		t.Fatalf("paper position rejected after reconcile: %v", err)
	}
	if err := safety.CheckNewPosition(RuntimeState{
		Mode: "paper", ExecutionVenue: "okx", ExecutionDemo: true,
	}); err != nil {
		t.Fatalf("OKX Demo position rejected after reconcile: %v", err)
	}
	if err := safety.CheckNewPosition(RuntimeState{
		Mode: "live", ExecutionVenue: "okx", ExecutionLive: true,
	}); err != nil {
		t.Fatalf("explicitly enabled OKX live position rejected after reconcile: %v", err)
	}
	for _, state := range []RuntimeState{
		{Mode: "live", ExecutionVenue: "paper"},
		{Mode: "live", ExecutionVenue: "binance", ExecutionLive: true},
		{Mode: "live", ExecutionVenue: "okx"},
		{Mode: "live", ExecutionVenue: "okx", ExecutionDemo: true},
		{Mode: "paper", ExecutionVenue: "binance"},
		{Mode: "paper", ExecutionVenue: "okx"},
		{Mode: "paper", ExecutionVenue: "okx", ExecutionLive: true},
	} {
		if err := safety.CheckNewPosition(state); !errors.Is(err, ErrLiveExecutionDisabled) {
			t.Fatalf("unsafe state %#v error = %v", state, err)
		}
	}
	if err := safety.CheckNewPosition(RuntimeState{Mode: "paper", ExecutionVenue: "paper", Kill: true}); !errors.Is(err, ErrKillSwitch) {
		t.Fatalf("kill guard = %v", err)
	}

	safety.BeginReconcile()
	if err := safety.CompleteReconcile(ReconcileReport{OK: false}, nil); !errors.Is(err, ErrReconciliationFailed) {
		t.Fatalf("failed reconcile error = %v", err)
	}
	if snapshot := safety.Snapshot(); !snapshot.Frozen || snapshot.ReconcileActive {
		t.Fatalf("unsafe snapshot after failure: %#v", snapshot)
	}
}

func TestReconcileCannotClearNewerConcurrentFreeze(t *testing.T) {
	state := NewSafetyState()
	state.BeginReconcile()
	state.Freeze("position monitor found a new margin breach")
	err := state.CompleteReconcile(ReconcileReport{OK: true}, nil)
	if !errors.Is(err, ErrReconciliationFrozen) {
		t.Fatalf("complete reconcile error = %v", err)
	}
	snapshot := state.Snapshot()
	if !snapshot.Frozen || snapshot.Reason != "position monitor found a new margin breach" || snapshot.ReconcileActive {
		t.Fatalf("newer freeze was overwritten: %+v", snapshot)
	}
}

func TestModePolicySeparatesPaperDemoAndLiveRiskScopes(t *testing.T) {
	t.Parallel()
	policy, err := ResolveModePolicy("paper")
	if err != nil {
		t.Fatal(err)
	}
	local := ExecutionTarget{
		VenueID: "paper", Kind: "paper", Environment: "paper", Writable: true,
	}
	demo := ExecutionTarget{
		VenueID: "okx", Kind: "cex", Environment: "demo", Writable: true,
	}
	if err := policy.ValidateExecution(local); err != nil {
		t.Fatalf("local Paper rejected: %v", err)
	}
	if err := policy.ValidateExecution(demo); err != nil {
		t.Fatalf("OKX Demo rejected: %v", err)
	}
	if paperScope, demoScope := policy.RiskStateScope(local), policy.RiskStateScope(demo); paperScope != "paper" || demoScope != "demo:okx" || paperScope == demoScope {
		t.Fatalf("unsafe scopes: paper=%q demo=%q", paperScope, demoScope)
	}
	live, err := ResolveModePolicy("live")
	if err != nil {
		t.Fatal(err)
	}
	if err := live.ValidateExecution(demo); !errors.Is(err, ErrLiveExecutionDisabled) {
		t.Fatalf("live policy unexpectedly allowed demo execution: %v", err)
	}
	okxLive := ExecutionTarget{
		VenueID: "okx", Kind: "cex", Environment: "live", Writable: true,
	}
	if err := live.ValidateExecution(okxLive); err != nil {
		t.Fatalf("live policy rejected writable OKX live target: %v", err)
	}
	if scope := live.RiskStateScope(okxLive); scope != "live:okx" ||
		scope == policy.RiskStateScope(demo) || scope == policy.RiskStateScope(local) {
		t.Fatalf("live risk scope is not isolated: %q", scope)
	}
	for _, target := range []ExecutionTarget{
		{VenueID: "binance", Kind: "cex", Environment: "live", Writable: true},
		{VenueID: "okx", Kind: "cex", Environment: "live", Writable: false},
		{VenueID: "okx", Kind: "onchain", Environment: "live", Writable: true},
	} {
		if err := live.ValidateExecution(target); !errors.Is(err, ErrLiveExecutionDisabled) {
			t.Fatalf("live policy accepted unsafe target %#v: %v", target, err)
		}
	}
	if err := policy.ValidateExecution(okxLive); !errors.Is(err, ErrLiveExecutionDisabled) {
		t.Fatalf("paper policy unexpectedly allowed live execution: %v", err)
	}
}

func TestSymbolLocksSerializeAndWaitingIsCancelable(t *testing.T) {
	t.Parallel()
	locks := NewSymbolLocks()
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- locks.Do(context.Background(), "BTC/USDT", func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	waitContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := locks.Do(waitContext, "BTC/USDT", func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting lock error = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	locks.mu.Lock()
	remaining := len(locks.locks)
	locks.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("idle symbol locks leaked: %d", remaining)
	}
}

func TestScannerSerializesConcurrentRunsForOneSymbol(t *testing.T) {
	t.Parallel()
	safety := NewSafetyState()
	if err := safety.CompleteReconcile(ReconcileReport{OK: true}, nil); err != nil {
		t.Fatal(err)
	}
	var active atomic.Int32
	var maximum atomic.Int32
	runner := func(ctx context.Context, _ string) error {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Millisecond):
			return nil
		}
	}
	scanner, err := NewScanner(ScannerConfig{
		Symbols: []string{"BTC/USDT"}, Interval: time.Second, Run: runner,
		Safety: safety, Locks: NewSymbolLocks(),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { done <- scanner.ScanOnce(context.Background()) }()
	go func() { done <- scanner.ScanOnce(context.Background()) }()
	for index := 0; index < 2; index++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent runs = %d", maximum.Load())
	}
}

func TestScannerNeverCallsRunnerForLiveState(t *testing.T) {
	t.Parallel()
	safety := NewSafetyState()
	if err := safety.CompleteReconcile(ReconcileReport{OK: true}, nil); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	scanner, err := NewScanner(ScannerConfig{
		Symbols: []string{"BTC/USDT"}, Interval: time.Second,
		Run:    func(context.Context, string) error { calls.Add(1); return nil },
		State:  func() RuntimeState { return RuntimeState{Mode: "live", ExecutionVenue: "paper"} },
		Safety: safety,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scanner.ScanOnce(context.Background()); !errors.Is(err, ErrLiveExecutionDisabled) {
		t.Fatalf("scan error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("live runner was called")
	}
}
