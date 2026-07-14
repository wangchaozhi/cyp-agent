package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/events"
	"github.com/wangchaozhi/cyp-agent/internal/orders"
	runtimecore "github.com/wangchaozhi/cyp-agent/internal/runtime"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

// remediationVenue simulates an OKX-like venue for the protective remediation
// path. It can be scripted to fail protective placement or to acknowledge a
// placement without actually keeping the algo order (the false-positive bug
// this path exists to catch).
type remediationVenue struct {
	mu                   sync.Mutex
	positions            []contracts.Position
	protective           []contracts.ProtectiveOrder
	protectivePlaceErrs  []error
	ackWithoutOrders     bool
	protectivePlaceCalls int
	verifyCalls          int
	cancelCalls          int
	reduceOnlyPlaced     []contracts.OrderIntent
	reconcileResults     map[string]contracts.ExecutionResult
}

func (v *remediationVenue) ID() string         { return "okx" }
func (v *remediationVenue) Kind() venue.Kind   { return venue.KindCEX }
func (v *remediationVenue) IsConfigured() bool { return true }
func (v *remediationVenue) Caps() venue.Caps {
	return venue.Caps{Perp: true, NativeProtectiveOrders: true}
}

func (v *remediationVenue) FetchTicker(context.Context, string) (contracts.Decimal, error) {
	return contracts.MustDecimal("2000"), nil
}

func (v *remediationVenue) FetchOHLCV(context.Context, string, string, int) ([]contracts.Candle, error) {
	return nil, errors.New("not implemented")
}

func (v *remediationVenue) FetchOrderBook(context.Context, string, int) (contracts.OrderBook, error) {
	return contracts.OrderBook{}, errors.New("not implemented")
}

func (v *remediationVenue) Positions(context.Context) ([]contracts.Position, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]contracts.Position(nil), v.positions...), nil
}

func (v *remediationVenue) Balances(context.Context) (contracts.Balances, error) {
	return contracts.Balances{
		QuoteCCY: "USDT", FreeQuote: contracts.MustDecimal("10000"),
		TotalQuote: contracts.MustDecimal("10000"),
	}, nil
}

func (v *remediationVenue) Preflight(context.Context, contracts.OrderIntent) (venue.PreflightReport, error) {
	return venue.PreflightReport{OK: true}, nil
}

// Place only accepts the emergency reduce-only close and flattens the book.
func (v *remediationVenue) Place(_ context.Context, intent contracts.OrderIntent) (contracts.ExecutionResult, error) {
	if !intent.ReduceOnly {
		return contracts.ExecutionResult{}, errors.New("remediation venue only accepts reduce-only orders")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.reduceOnlyPlaced = append(v.reduceOnlyPlaced, intent)
	v.positions = nil
	return contracts.ExecutionResult{
		ClientID: intent.ClientID, Status: contracts.OrderStatusFilled,
		FilledBase: contracts.MustDecimal("0.1"), AvgPrice: intent.Price,
	}, nil
}

func (v *remediationVenue) Cancel(context.Context, string) error { return nil }

func (v *remediationVenue) ProtectiveOrders(context.Context, string) ([]contracts.ProtectiveOrder, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.verifyCalls++
	return append([]contracts.ProtectiveOrder(nil), v.protective...), nil
}

func (v *remediationVenue) CancelProtectiveOrders(context.Context, string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cancelCalls++
	v.protective = nil
	return nil
}

func (v *remediationVenue) PlaceProtectiveOrders(
	_ context.Context, clientID string, _ string, side contracts.Side, _ contracts.MarginMode,
	stopLoss *contracts.Decimal, takeProfit *contracts.Decimal,
) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.protectivePlaceCalls++
	if len(v.protectivePlaceErrs) > 0 {
		err := v.protectivePlaceErrs[0]
		v.protectivePlaceErrs = v.protectivePlaceErrs[1:]
		if err != nil {
			return err
		}
	}
	if v.ackWithoutOrders {
		return nil
	}
	if stopLoss != nil {
		v.protective = append(v.protective, contracts.ProtectiveOrder{
			Kind: "stop_loss", OrderID: "remediated-sl",
			ClientID: venue.SanitizeOKXClientID(clientID, "protect"), PositionSide: side,
			TriggerPrice: *stopLoss, ReduceOnly: true, FullClose: true,
		})
	}
	if takeProfit != nil {
		v.protective = append(v.protective, contracts.ProtectiveOrder{
			Kind: "take_profit", OrderID: "remediated-tp",
			ClientID: venue.SanitizeOKXClientID(clientID, "protect"), PositionSide: side,
			TriggerPrice: *takeProfit, ReduceOnly: true, FullClose: true,
		})
	}
	return nil
}

// ReconcileOrder returns the scripted authoritative remote state, mirroring
// the OKX clOrdId lookup used after ambiguous submissions and restarts.
func (v *remediationVenue) ReconcileOrder(_ context.Context, intent contracts.OrderIntent) (contracts.ExecutionResult, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	result, found := v.reconcileResults[intent.ClientID]
	return result, found, nil
}

type recordingAlerter struct {
	mu     sync.Mutex
	alerts []string
}

func (a *recordingAlerter) Alert(_ context.Context, level, message string, _ map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, level+":"+message)
	return nil
}

func (a *recordingAlerter) joined() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.alerts, "\n")
}

func newRemediationService(t *testing.T, fake *remediationVenue) (*Service, *runtimecore.SafetyState, *recordingAlerter) {
	t.Helper()
	safety := runtimecore.NewSafetyState()
	safety.BeginReconcile()
	if err := safety.CompleteReconcile(runtimecore.ReconcileReport{OK: true}, nil); err != nil {
		t.Fatal(err)
	}
	alerter := &recordingAlerter{}
	bus := events.NewBus(64)
	t.Cleanup(bus.Close)
	service := &Service{
		venue: fake, journal: orders.NewJournal(), events: bus,
		safety: safety, alerter: alerter,
	}
	return service, safety, alerter
}

func protectedIntent(clientID string) contracts.OrderIntent {
	stop := contracts.MustDecimal("1800")
	mark := contracts.MustDecimal("2000")
	return contracts.OrderIntent{
		ClientID: clientID, Symbol: "ETH/USDT:USDT", Venue: "okx",
		Side: contracts.SideLong, Instrument: contracts.InstrumentPerp,
		OrderType: contracts.EntryTypeMarket, SizeQuote: contracts.MustDecimal("200"),
		Price: &mark, Leverage: 2, MarginMode: contracts.MarginModeIsolated, StopLoss: &stop,
	}
}

// journalUnprotectedFill drives an entry through open -> submitting -> filled
// with no exchange-verified protection; recordExecution must fail closed into
// protective_failed, which is the trigger for remediation.
func journalUnprotectedFill(t *testing.T, service *Service, runID string, intent contracts.OrderIntent) {
	t.Helper()
	if err := service.journal.Open(runID+":open", intent); err != nil {
		t.Fatal(err)
	}
	if err := service.journal.Transition(runID+":submit", intent.ClientID, contracts.OrderStatusSubmitting, nil, ""); err != nil {
		t.Fatal(err)
	}
	execution := contracts.ExecutionResult{
		ClientID: intent.ClientID, Status: contracts.OrderStatusFilled,
		FilledBase: contracts.MustDecimal("0.1"), AvgPrice: intent.Price,
	}
	if err := service.recordExecution(context.Background(), runID, intent, execution); err != nil {
		t.Fatal(err)
	}
	order, ok := service.journal.Get(intent.ClientID)
	if !ok || order.Status != contracts.OrderStatusProtectiveFailed {
		t.Fatalf("unverified fill journaled as %s, want protective_failed", order.Status)
	}
}

// A terminalized partial fill is still an open position. It must enter the
// same protection/remediation lifecycle as a full fill.
func TestPartialFillWithoutVerifiedProtectionFailsClosed(t *testing.T) {
	fake := &remediationVenue{}
	service, _, _ := newRemediationService(t, fake)
	intent := protectedIntent("entry-partial")
	if err := service.journal.Open("partial:open", intent); err != nil {
		t.Fatal(err)
	}
	if err := service.journal.Transition("partial:submit", intent.ClientID, contracts.OrderStatusSubmitting, nil, ""); err != nil {
		t.Fatal(err)
	}
	execution := contracts.ExecutionResult{
		ClientID: intent.ClientID, Status: contracts.OrderStatusPartiallyFilled,
		FilledBase: contracts.MustDecimal("0.04"), AvgPrice: intent.Price,
	}
	if err := service.recordExecution(context.Background(), "partial", intent, execution); err != nil {
		t.Fatal(err)
	}
	tracked, ok := service.journal.Get(intent.ClientID)
	if !ok || tracked.Status != contracts.OrderStatusProtectiveFailed {
		t.Fatalf("partial fill journal state = %+v (ok=%t), want protective_failed", tracked, ok)
	}
}

func TestOKXProtectionVerificationRejectsUnrelatedOrUnderCoveredOrders(t *testing.T) {
	service, _, _ := newRemediationService(t, &remediationVenue{})
	intent := protectedIntent("entry-strict")
	filled := contracts.MustDecimal("0.1")
	expectedID := venue.SanitizeOKXClientID(intent.ClientID, "protect")
	valid := contracts.ProtectiveOrder{
		Kind: "stop_loss", OrderID: "sl", ClientID: expectedID,
		PositionSide: contracts.SideLong, TriggerPrice: *intent.StopLoss,
		ReduceOnly: true, FullClose: true,
	}
	if !service.protectionSatisfied(intent, filled, []contracts.ProtectiveOrder{valid}) {
		t.Fatal("matching full-close protection was rejected")
	}
	cases := map[string]contracts.ProtectiveOrder{
		"unrelated client": func() contracts.ProtectiveOrder { order := valid; order.ClientID = "other"; return order }(),
		"wrong side": func() contracts.ProtectiveOrder {
			order := valid
			order.PositionSide = contracts.SideShort
			return order
		}(),
		"not reduce only": func() contracts.ProtectiveOrder { order := valid; order.ReduceOnly = false; return order }(),
		"under covered": func() contracts.ProtectiveOrder {
			order := valid
			order.FullClose = false
			order.SizeBase = contracts.MustDecimal("0.09")
			return order
		}(),
		"wrong trigger": func() contracts.ProtectiveOrder {
			order := valid
			order.TriggerPrice = contracts.MustDecimal("1799")
			return order
		}(),
	}
	for name, order := range cases {
		t.Run(name, func(t *testing.T) {
			if service.protectionSatisfied(intent, filled, []contracts.ProtectiveOrder{order}) {
				t.Fatalf("unsafe protection accepted: %+v", order)
			}
		})
	}
}

// TestProtectiveRemediationRecoversAfterTransientFailure covers the happy
// remediation path: the first re-placement hits a network fault, the second
// succeeds and is verified against the exchange before the journal advances.
func TestProtectiveRemediationRecoversAfterTransientFailure(t *testing.T) {
	fake := &remediationVenue{
		positions: []contracts.Position{{
			Symbol: "ETH/USDT:USDT", Venue: "okx", Side: contracts.SideLong,
			Instrument: contracts.InstrumentPerp, SizeBase: contracts.MustDecimal("0.1"),
			EntryPrice: contracts.MustDecimal("2000"), Leverage: 2,
		}},
		protectivePlaceErrs: []error{errors.New("injected network fault")},
	}
	service, safety, alerter := newRemediationService(t, fake)
	intent := protectedIntent("entry-recover")
	journalUnprotectedFill(t, service, "run-recover", intent)

	if err := service.remediateProtection(context.Background(), "run-recover", intent, contracts.MustDecimal("2000")); err != nil {
		t.Fatalf("remediation error = %v", err)
	}
	order, _ := service.journal.Get(intent.ClientID)
	if order.Status != contracts.OrderStatusProtectivePlaced {
		t.Fatalf("remediated status = %s", order.Status)
	}
	if fake.protectivePlaceCalls != 2 {
		t.Fatalf("place attempts = %d, want 2", fake.protectivePlaceCalls)
	}
	if len(fake.reduceOnlyPlaced) != 0 {
		t.Fatalf("successful remediation must not flatten: %+v", fake.reduceOnlyPlaced)
	}
	if snapshot := safety.Snapshot(); snapshot.Frozen {
		t.Fatalf("successful remediation froze safety: %+v", snapshot)
	}
	if !strings.Contains(alerter.joined(), "warning:protective_remediated") {
		t.Fatalf("missing remediation alert, got %q", alerter.joined())
	}
}

// TestProtectiveRemediationExhaustionFlattensAndFreezes injects the worst
// case: the exchange acknowledges every re-placement but never actually keeps
// the algo order. After bounded retries the position must be emergency
// flattened, the journal closed through flattening, safety frozen, and
// critical alerts emitted.
func TestProtectiveRemediationExhaustionFlattensAndFreezes(t *testing.T) {
	fake := &remediationVenue{
		positions: []contracts.Position{{
			Symbol: "ETH/USDT:USDT", Venue: "okx", Side: contracts.SideLong,
			Instrument: contracts.InstrumentPerp, SizeBase: contracts.MustDecimal("0.1"),
			EntryPrice: contracts.MustDecimal("2000"), Leverage: 2,
		}},
		ackWithoutOrders: true,
	}
	service, safety, alerter := newRemediationService(t, fake)
	intent := protectedIntent("entry-exhaust")
	journalUnprotectedFill(t, service, "run-exhaust", intent)

	err := service.remediateProtection(context.Background(), "run-exhaust", intent, contracts.MustDecimal("2000"))
	if err == nil || !strings.Contains(err.Error(), "position flattened") {
		t.Fatalf("exhausted remediation error = %v", err)
	}
	if fake.protectivePlaceCalls != protectiveRemediationAttempts {
		t.Fatalf("place attempts = %d, want %d", fake.protectivePlaceCalls, protectiveRemediationAttempts)
	}
	if len(fake.reduceOnlyPlaced) != 1 || !fake.reduceOnlyPlaced[0].ReduceOnly ||
		fake.reduceOnlyPlaced[0].Symbol != intent.Symbol {
		t.Fatalf("emergency flatten orders = %+v", fake.reduceOnlyPlaced)
	}
	entry, _ := service.journal.Get(intent.ClientID)
	if entry.Status != contracts.OrderStatusClosed {
		t.Fatalf("entry lifecycle after flatten = %s, want closed", entry.Status)
	}
	flatten, ok := service.journal.Get("flatten-run-exhaust")
	if !ok || flatten.Status != contracts.OrderStatusClosed {
		t.Fatalf("flatten lifecycle = %+v (ok=%t)", flatten, ok)
	}
	if positions, _ := fake.Positions(context.Background()); len(positions) != 0 {
		t.Fatalf("position survived emergency flatten: %+v", positions)
	}
	snapshot := safety.Snapshot()
	if !snapshot.Frozen || !strings.Contains(snapshot.Reason, "protective remediation exhausted") {
		t.Fatalf("safety after exhaustion = %+v", snapshot)
	}
	joined := alerter.joined()
	if !strings.Contains(joined, "critical:protective_remediation_exhausted") ||
		!strings.Contains(joined, "critical:emergency_flattened") {
		t.Fatalf("missing critical alerts, got %q", joined)
	}
	if fake.cancelCalls == 0 {
		t.Fatal("flatten finalization must clear residual protective orders")
	}
}

// TestProtectiveRemediationExhaustionWithFlatBookClosesJournalOnly verifies
// the degenerate case where the position disappeared (e.g. the stop actually
// existed and fired, or a manual close raced remediation): no reduce-only
// order is sent, protection is swept, and the journal is closed.
func TestProtectiveRemediationExhaustionWithFlatBookClosesJournalOnly(t *testing.T) {
	fake := &remediationVenue{ackWithoutOrders: true}
	service, safety, alerter := newRemediationService(t, fake)
	intent := protectedIntent("entry-flat")
	journalUnprotectedFill(t, service, "run-flat", intent)

	err := service.remediateProtection(context.Background(), "run-flat", intent, contracts.MustDecimal("2000"))
	if err == nil || !strings.Contains(err.Error(), "position flattened") {
		t.Fatalf("exhausted remediation error = %v", err)
	}
	if len(fake.reduceOnlyPlaced) != 0 {
		t.Fatalf("flat book must not place reduce-only orders: %+v", fake.reduceOnlyPlaced)
	}
	entry, _ := service.journal.Get(intent.ClientID)
	if entry.Status != contracts.OrderStatusClosed {
		t.Fatalf("entry lifecycle = %s, want closed", entry.Status)
	}
	if fake.cancelCalls == 0 {
		t.Fatal("residual protective orders must be swept even when already flat")
	}
	if snapshot := safety.Snapshot(); !snapshot.Frozen {
		t.Fatal("exhaustion must freeze safety even when the book is flat")
	}
	if !strings.Contains(alerter.joined(), "critical:protective_remediation_exhausted") {
		t.Fatalf("missing critical alert, got %q", alerter.joined())
	}
}

// TestCrashReplayReconcilesLiveOrderLifecycles simulates a process crash on a
// live venue: the persisted event log is replayed into a fresh journal and
// startup reconciliation consumes the unresolved orders. Submissions that
// were interrupted mid-flight are restored via the authoritative remote
// lookup (never re-submitted), unprotected fills surface as discrepancies,
// and stale lifecycles over a flat book are closed with protection swept.
func TestCrashReplayReconcilesLiveOrderLifecycles(t *testing.T) {
	interrupted := protectedIntent("crash-submitting")
	unprotected := protectedIntent("crash-unprotected")
	stale := protectedIntent("crash-stale")
	stale.Symbol = "BTC/USDT:USDT"

	log := make([]orders.Event, 0, 16)
	appendEvent := func(id string, clientID string, status contracts.OrderStatus, intent *contracts.OrderIntent) {
		log = append(log, orders.Event{EventID: id, ClientID: clientID, Status: status, Intent: intent})
	}
	appendEvent("a:open", interrupted.ClientID, contracts.OrderStatusNew, &interrupted)
	appendEvent("a:submit", interrupted.ClientID, contracts.OrderStatusSubmitting, nil)
	appendEvent("b:open", unprotected.ClientID, contracts.OrderStatusNew, &unprotected)
	appendEvent("b:submit", unprotected.ClientID, contracts.OrderStatusSubmitting, nil)
	appendEvent("b:fill", unprotected.ClientID, contracts.OrderStatusFilled, nil)
	appendEvent("b:protect", unprotected.ClientID, contracts.OrderStatusProtectiveFailed, nil)
	appendEvent("c:open", stale.ClientID, contracts.OrderStatusNew, &stale)
	appendEvent("c:submit", stale.ClientID, contracts.OrderStatusSubmitting, nil)
	appendEvent("c:fill", stale.ClientID, contracts.OrderStatusFilled, nil)
	appendEvent("c:protect", stale.ClientID, contracts.OrderStatusProtectiveFailed, nil)
	appendEvent("c:flatten", stale.ClientID, contracts.OrderStatusFlattening, nil)

	journal, err := orders.Replay(log)
	if err != nil {
		t.Fatalf("crash replay failed: %v", err)
	}
	if unresolvedCount := len(journal.Unresolved()); unresolvedCount != 3 {
		t.Fatalf("unresolved after replay = %d, want 3", unresolvedCount)
	}

	remoteFill := contracts.ExecutionResult{
		ClientID: interrupted.ClientID, Status: contracts.OrderStatusFilled,
		FilledBase: contracts.MustDecimal("0.1"),
		ProtectiveOrders: contracts.List[contracts.ProtectiveOrder]{{
			Kind: "stop_loss", OrderID: "remote-sl",
			ClientID:     venue.SanitizeOKXClientID(interrupted.ClientID, "protect"),
			PositionSide: contracts.SideLong, TriggerPrice: contracts.MustDecimal("1800"),
			ReduceOnly: true, FullClose: true,
		}},
	}
	fake := &remediationVenue{
		positions: []contracts.Position{
			{Symbol: interrupted.Symbol, Venue: "okx", Side: contracts.SideLong,
				Instrument: contracts.InstrumentPerp, SizeBase: contracts.MustDecimal("0.1"),
				EntryPrice: contracts.MustDecimal("2000")},
		},
		reconcileResults: map[string]contracts.ExecutionResult{interrupted.ClientID: remoteFill},
	}
	service, _, _ := newRemediationService(t, fake)
	service.journal = journal

	discrepancies, err := service.ReconcileOrders(context.Background(), fake.positions)
	if err != nil {
		t.Fatalf("startup order reconcile failed: %v", err)
	}
	// The interrupted submission was found filled and protected remotely.
	restored, _ := journal.Get(interrupted.ClientID)
	if restored.Status != contracts.OrderStatusProtectivePlaced {
		t.Fatalf("interrupted submission restored as %s", restored.Status)
	}
	// The unprotected fill with a live position must be escalated, not hidden.
	joined := strings.Join(discrepancies, "\n")
	if !strings.Contains(joined, unprotected.ClientID) {
		t.Fatalf("unprotected fill missing from discrepancies: %v", discrepancies)
	}
	still, _ := journal.Get(unprotected.ClientID)
	if still.Status != contracts.OrderStatusProtectiveFailed {
		t.Fatalf("unprotected fill status = %s, want protective_failed", still.Status)
	}
	// The stale flattening lifecycle over a flat book is closed and swept.
	closed, _ := journal.Get(stale.ClientID)
	if closed.Status != contracts.OrderStatusClosed {
		t.Fatalf("stale flattening status = %s, want closed", closed.Status)
	}
	if fake.cancelCalls == 0 {
		t.Fatal("flat-book reconciliation must sweep residual protective orders")
	}
	// Replaying the same persisted log again is idempotent by event ID.
	for _, event := range log {
		if err := journal.Apply(event); err != nil {
			t.Fatalf("re-applying persisted event %s failed: %v", event.EventID, err)
		}
	}
}
