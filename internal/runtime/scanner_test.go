package runtime

import (
	"context"
	"reflect"
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
