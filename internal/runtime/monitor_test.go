package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/events"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

func openPaperPosition(t *testing.T, target *venue.PaperVenue, withStop bool) {
	t.Helper()
	if err := target.SetMarkPrice("BTC/USDT", contracts.MustDecimal("100")); err != nil {
		t.Fatal(err)
	}
	intent := contracts.OrderIntent{
		ClientID: "open", Symbol: "BTC/USDT", Venue: "paper", Side: contracts.SideLong,
		Instrument: contracts.InstrumentSpot, OrderType: contracts.EntryTypeMarket,
		SizeQuote: contracts.MustDecimal("100"), Leverage: 1,
		MarginMode: contracts.MarginModeIsolated,
	}
	if withStop {
		stop := contracts.MustDecimal("90")
		intent.StopLoss = &stop
	}
	result, err := target.Place(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != contracts.OrderStatusFilled {
		t.Fatalf("open result = %#v", result)
	}
}

func TestVenueReconcilerRequiresActualStopLoss(t *testing.T) {
	t.Parallel()
	withoutStop := venue.NewPaperVenue()
	openPaperPosition(t, withoutStop, false)
	reconciler, err := NewVenueReconciler(withoutStop, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := reconciler.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.OK || len(report.ProtectiveGaps) != 1 {
		t.Fatalf("unprotected report = %#v", report)
	}

	withStop := venue.NewPaperVenue()
	openPaperPosition(t, withStop, true)
	bus := events.NewBus(4)
	subscription := bus.Subscribe(1)
	defer subscription.Cancel()
	reconciler, err = NewVenueReconciler(withStop, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err = reconciler.Reconcile(context.Background())
	if err != nil || !report.OK || len(report.ProtectiveGaps) != 0 {
		t.Fatalf("protected report=%#v error=%v", report, err)
	}
	select {
	case event := <-subscription.C:
		if event.Type != "reconciled" {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("reconcile event missing")
	}
}

type captureAlerter struct {
	mu     sync.Mutex
	calls  int
	fields map[string]any
}

func (alerter *captureAlerter) Alert(_ context.Context, _, _ string, fields map[string]any) error {
	alerter.mu.Lock()
	defer alerter.mu.Unlock()
	alerter.calls++
	alerter.fields = fields
	return nil
}

func (alerter *captureAlerter) Calls() int {
	alerter.mu.Lock()
	defer alerter.mu.Unlock()
	return alerter.calls
}

func TestPositionMonitorReportsProtectionAndMovement(t *testing.T) {
	t.Parallel()
	target := venue.NewPaperVenue()
	openPaperPosition(t, target, false)
	alerter := &captureAlerter{}
	monitor, err := NewPositionMonitor(PositionMonitorConfig{
		Venue: target, Interval: time.Second, Alerter: alerter,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := monitor.CheckOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Positions) != 1 || !containsAlert(first.Alerts, "缺少止损保护单") {
		t.Fatalf("first report = %#v", first)
	}
	if err := target.SetMarkPrice("BTC/USDT", contracts.MustDecimal("110")); err != nil {
		t.Fatal(err)
	}
	second, err := monitor.CheckOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !containsAlert(second.Alerts, "异常波动") || alerter.Calls() != 2 {
		t.Fatalf("second report=%#v alert calls=%d", second, alerter.Calls())
	}
}

func containsAlert(alerts []string, fragment string) bool {
	for _, alert := range alerts {
		if strings.Contains(alert, fragment) {
			return true
		}
	}
	return false
}

type nonPaperVenue struct {
	positionsCalled bool
}

func (*nonPaperVenue) ID() string         { return "binance" }
func (*nonPaperVenue) Kind() venue.Kind   { return venue.KindCEX }
func (*nonPaperVenue) Caps() venue.Caps   { return venue.Caps{} }
func (*nonPaperVenue) IsConfigured() bool { return true }
func (*nonPaperVenue) FetchTicker(context.Context, string) (contracts.Decimal, error) {
	return contracts.Zero(), nil
}
func (*nonPaperVenue) FetchOHLCV(context.Context, string, string, int) ([]contracts.Candle, error) {
	return []contracts.Candle{}, nil
}
func (*nonPaperVenue) FetchOrderBook(context.Context, string, int) (contracts.OrderBook, error) {
	return contracts.OrderBook{}, nil
}
func (target *nonPaperVenue) Positions(context.Context) ([]contracts.Position, error) {
	target.positionsCalled = true
	return []contracts.Position{}, nil
}
func (*nonPaperVenue) Balances(context.Context) (contracts.Balances, error) {
	return contracts.Balances{}, nil
}
func (*nonPaperVenue) Preflight(context.Context, contracts.OrderIntent) (venue.PreflightReport, error) {
	return venue.PreflightReport{}, nil
}
func (*nonPaperVenue) Place(context.Context, contracts.OrderIntent) (contracts.ExecutionResult, error) {
	return contracts.ExecutionResult{}, errors.New("must never place")
}
func (*nonPaperVenue) Cancel(context.Context, string) error { return nil }

func TestRuntimeRefusesNonPaperVenueBeforeReadingOrPlacing(t *testing.T) {
	t.Parallel()
	target := &nonPaperVenue{}
	reconciler, err := NewVenueReconciler(target, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background()); !errors.Is(err, ErrLiveExecutionDisabled) {
		t.Fatalf("reconcile error = %v", err)
	}
	monitor, err := NewPositionMonitor(PositionMonitorConfig{Venue: target, Interval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := monitor.CheckOnce(context.Background()); !errors.Is(err, ErrLiveExecutionDisabled) {
		t.Fatalf("monitor error = %v", err)
	}
	if target.positionsCalled {
		t.Fatal("non-paper positions were read")
	}
}
