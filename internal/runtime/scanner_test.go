package runtime

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestScannerUsesCurrentSymbolsAndAutomationGate(t *testing.T) {
	enabled := false
	symbols := []string{"BTC/USDT"}
	called := make([]string, 0)
	safety := NewSafetyState()
	safety.BeginReconcile()
	if err := safety.CompleteReconcile(ReconcileReport{OK: true}, nil); err != nil {
		t.Fatal(err)
	}
	scanner, err := NewScanner(ScannerConfig{
		Symbols: []string{"FALLBACK/USDT"}, SymbolProvider: func() []string { return symbols },
		Interval: time.Second, Enabled: func() bool { return enabled },
		Safety: safety,
		Run: func(_ context.Context, symbol string) error {
			called = append(called, symbol)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scanner.ScanOnce(context.Background()); err != nil || len(called) != 0 {
		t.Fatalf("disabled scan called=%v err=%v", called, err)
	}
	enabled = true
	symbols = []string{"ETH/USDT", "SOL/USDT", "ETH/USDT"}
	if err := scanner.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ETH/USDT", "SOL/USDT"}; !reflect.DeepEqual(called, want) {
		t.Fatalf("called=%v want=%v", called, want)
	}
}

func TestScannerFrequencyChangeResetsRunningSchedule(t *testing.T) {
	var intervalNanos atomic.Int64
	intervalNanos.Store(int64(time.Hour))
	runs := make(chan struct{}, 2)
	safety := NewSafetyState()
	if err := safety.CompleteReconcile(ReconcileReport{OK: true}, nil); err != nil {
		t.Fatal(err)
	}
	scanner, err := NewScanner(ScannerConfig{
		Symbols: []string{"BTC/USDT"}, Interval: time.Hour,
		IntervalProvider: func() time.Duration { return time.Duration(intervalNanos.Load()) },
		Safety:           safety,
		Run: func(context.Context, string) error {
			runs <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scanner.Run(ctx) }()
	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("initial scan did not run")
	}
	intervalNanos.Store(int64(20 * time.Millisecond))
	scanner.NotifyScheduleChanged()
	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("frequency change did not reset scanner timer")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scanner did not stop")
	}
}
