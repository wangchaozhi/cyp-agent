package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type reconcileFunc func(context.Context) (ReconcileReport, error)

func (function reconcileFunc) Reconcile(ctx context.Context) (ReconcileReport, error) {
	return function(ctx)
}

func TestEngineReconcileFailureKeepsFrozenAndDoesNotStartScanner(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	scanner, err := NewScanner(ScannerConfig{
		Symbols: []string{"BTC/USDT"}, Interval: time.Millisecond,
		Run: func(context.Context, string) error { calls.Add(1); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(EngineConfig{
		Reconciler: reconcileFunc(func(context.Context) (ReconcileReport, error) {
			return ReconcileReport{Positions: []contracts.Position{}, OK: false}, errors.New("remote unavailable")
		}),
		Scanner: scanner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background()); !errors.Is(err, ErrReconciliationFailed) {
		t.Fatalf("start error = %v", err)
	}
	if !engine.Safety().Snapshot().Frozen || engine.Started() || calls.Load() != 0 {
		t.Fatalf("unsafe state: snapshot=%#v started=%v calls=%d",
			engine.Safety().Snapshot(), engine.Started(), calls.Load())
	}
}

func TestEngineCanStartDegradedWithoutOpeningScanner(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	scanner, err := NewScanner(ScannerConfig{
		Symbols: []string{"BTC/USDT"}, Interval: time.Millisecond,
		Run: func(context.Context, string) error { calls.Add(1); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(EngineConfig{
		Reconciler: reconcileFunc(func(context.Context) (ReconcileReport, error) {
			return ReconcileReport{
				Positions:      []contracts.Position{{Symbol: "BTC/USDT"}},
				ProtectiveGaps: []string{"BTC/USDT missing stop"}, OK: false,
			}, nil
		}),
		Scanner: scanner, AllowDegradedStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("degraded start failed: %v", err)
	}
	if !engine.Started() || !engine.Safety().Snapshot().Frozen || calls.Load() != 0 {
		t.Fatalf("unsafe degraded state: started=%v frozen=%v calls=%d",
			engine.Started(), engine.Safety().Snapshot().Frozen, calls.Load())
	}
	stopContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.Stop(stopContext); err != nil {
		t.Fatal(err)
	}
}

func TestEngineStartsAfterReconcileAndStopsOnContext(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	scanner, err := NewScanner(ScannerConfig{
		Symbols: []string{"BTC/USDT"}, Interval: 2 * time.Millisecond,
		Run: func(context.Context, string) error { calls.Add(1); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(EngineConfig{
		Reconciler: reconcileFunc(func(context.Context) (ReconcileReport, error) {
			return ReconcileReport{Positions: []contracts.Position{}, Discrepancies: []string{}, ProtectiveGaps: []string{}, OK: true}, nil
		}),
		Scanner: scanner,
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	if err := engine.Start(parent); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("scanner never ran")
	}
	cancel()
	stopContext, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := engine.Stop(stopContext); err != nil {
		t.Fatal(err)
	}
	if engine.Started() {
		t.Fatal("engine remained started")
	}
}

func TestStopCancelsStartupReconciliation(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	engine, err := NewEngine(EngineConfig{
		Reconciler: reconcileFunc(func(ctx context.Context) (ReconcileReport, error) {
			close(entered)
			<-ctx.Done()
			return ReconcileReport{Positions: []contracts.Position{}}, ctx.Err()
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	startResult := make(chan error, 1)
	go func() { startResult <- engine.Start(context.Background()) }()
	<-entered
	stopContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.Stop(stopContext); err != nil {
		t.Fatal(err)
	}
	if err := <-startResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("start error = %v", err)
	}
	if !engine.Safety().Snapshot().Frozen {
		t.Fatal("canceled reconcile unfroze safety state")
	}
}
