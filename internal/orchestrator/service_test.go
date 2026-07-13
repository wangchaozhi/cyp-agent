package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/approval"
	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/control"
	"github.com/wangchaozhi/cyp-agent/internal/events"
	"github.com/wangchaozhi/cyp-agent/internal/metrics"
	"github.com/wangchaozhi/cyp-agent/internal/orchestrator"
	"github.com/wangchaozhi/cyp-agent/internal/riskstate"
	runtimecore "github.com/wangchaozhi/cyp-agent/internal/runtime"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

// bullishSource yields an accelerating uptrend so analysts and the strategist
// deterministically emit a long proposal.
type bullishSource struct{}

func (bullishSource) Snapshot(ctx context.Context, symbol string) (contracts.MarketSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return contracts.MarketSnapshot{}, err
	}
	candles := make(contracts.List[contracts.Candle], 80)
	for index := range candles {
		price := contracts.NewDecimalFromInt64(int64(100 + index*index))
		candles[index] = contracts.Candle{
			TS: time.Unix(int64(index*3600), 0).UTC(), Open: price, High: price,
			Low: price, Close: price, Volume: contracts.MustDecimal("100"),
		}
	}
	return contracts.MarketSnapshot{
		Symbol: symbol, Venue: "test", TS: time.Now().UTC(), OHLCV: candles,
	}, nil
}

// emptySource returns a snapshot without candles, which must always resolve
// to a flat proposal.
type emptySource struct{}

func (emptySource) Snapshot(ctx context.Context, symbol string) (contracts.MarketSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return contracts.MarketSnapshot{}, err
	}
	return contracts.MarketSnapshot{Symbol: symbol, Venue: "test", TS: time.Now().UTC()}, nil
}

// failingSource forces the orchestrator onto its fallback data path.
type failingSource struct{}

func (failingSource) Snapshot(context.Context, string) (contracts.MarketSnapshot, error) {
	return contracts.MarketSnapshot{}, errors.New("primary feed unavailable")
}

type testHarness struct {
	service *orchestrator.Service
	gate    *approval.PendingGate
	venue   *venue.PaperVenue
}

func newHarness(t *testing.T, mutate func(*config.Settings), options ...orchestrator.Option) testHarness {
	t.Helper()
	settings := config.DefaultSettings()
	settings.Approval = "dashboard"
	settings.Risk.ApprovalTimeoutSeconds = 2
	if mutate != nil {
		mutate(&settings)
	}
	state := control.New(settings)
	bus := events.NewBus(1000)
	paper := venue.NewPaperVenue()
	timeout := time.Duration(settings.Risk.ApprovalTimeoutSeconds) * time.Second
	gate := approval.NewPendingGate(timeout, bus)
	service := orchestrator.New(context.Background(), state, paper, bus, gate, metrics.NewRuns(), options...)
	t.Cleanup(func() {
		service.Close()
		bus.Close()
	})
	return testHarness{service: service, gate: gate, venue: paper}
}

func resolveWhenPending(t *testing.T, gate *approval.PendingGate, runID string, request contracts.ApprovalRequest) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for gate.PendingCount() == 0 {
		if time.Now().After(deadline) {
			t.Error("run never reached the approval gate")
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := gate.Resolve(runID, request); err != nil {
		t.Errorf("gate.Resolve() error = %v", err)
	}
}

func TestRunOnceRejectsEmptySymbol(t *testing.T) {
	harness := newHarness(t, nil, orchestrator.WithDataSource(bullishSource{}))
	result := harness.service.RunOnce(context.Background(), "run-empty", "   ")
	if result.Status != contracts.RunError || result.Error == nil {
		t.Fatalf("empty symbol result = %+v", result)
	}
	if !strings.Contains(*result.Error, "symbol") {
		t.Fatalf("unexpected error message: %s", *result.Error)
	}
}

func TestRunOnceWithoutCandlesIsNoTradeAndSetsReferenceMark(t *testing.T) {
	harness := newHarness(t, nil, orchestrator.WithDataSource(emptySource{}))
	result := harness.service.RunOnce(context.Background(), "run-flat", "BTC/USDT")
	if result.Status != contracts.RunNoTrade {
		t.Fatalf("status = %s, error = %v", result.Status, result.Error)
	}
	if result.Proposal == nil || result.Proposal.Side != contracts.SideFlat {
		t.Fatalf("expected flat proposal, got %+v", result.Proposal)
	}
	mark, ok := harness.service.Mark("BTC/USDT")
	if !ok || mark.String() != "60000" {
		t.Fatalf("BTC reference mark = %s (ok=%v)", mark, ok)
	}
}

func TestRunOnceAutoApprovalExecutes(t *testing.T) {
	harness := newHarness(t, func(settings *config.Settings) {
		settings.Automation.Enabled = true
		settings.Automation.ApprovalEnabled = true
		settings.Automation.MinConfidence = 0
		settings.Automation.MinRewardRisk = 1
		settings.AutoSymbols = "BTC/USDT"
		settings.Automation.MaxRiskScore = 1
		settings.Automation.MaxQuote = contracts.MustDecimal("10000")
	}, orchestrator.WithDataSource(bullishSource{}))

	result := harness.service.RunOnce(context.Background(), "run-auto", "BTC/USDT")
	if result.Status != contracts.RunExecuted {
		t.Fatalf("status = %s, error = %v", result.Status, result.Error)
	}
	if result.Decision == nil || result.Decision.Operator != "auto-policy" {
		t.Fatalf("expected auto-policy approval, got %+v", result.Decision)
	}
	if result.Execution == nil || result.Execution.Status != contracts.OrderStatusFilled {
		t.Fatalf("expected filled execution, got %+v", result.Execution)
	}
	if result.Review == nil {
		t.Fatal("executed run must include a review")
	}
	positions, err := harness.venue.Positions(context.Background())
	if err != nil || len(positions) != 1 {
		t.Fatalf("positions after execution = %v (err=%v)", positions, err)
	}
	mark, ok := harness.service.Mark("BTC/USDT")
	if !ok || !mark.IsPositive() {
		t.Fatalf("mark after run = %s (ok=%v)", mark, ok)
	}
}

func TestRunOnceAutomaticallyAddsToProfitableSameSidePosition(t *testing.T) {
	harness := newHarness(t, func(settings *config.Settings) {
		settings.Automation.Enabled = true
		settings.Automation.ScanEnabled = true
		settings.Automation.EntryEnabled = true
		settings.Automation.ApprovalEnabled = true
		settings.Automation.AddEnabled = true
		settings.Automation.MinConfidence = 0
		settings.Automation.MinRewardRisk = 1
		settings.Automation.AddMinConfidence = 0
		settings.Automation.AddMinProfitR = 0.1
		settings.Automation.AddCooldownMinutes = 0
		settings.Automation.MinEntryQuote = contracts.MustDecimal("1")
		settings.Automation.MaxQuote = contracts.MustDecimal("200")
		settings.AutoSymbols = "BTC/USDT"
	}, orchestrator.WithDataSource(bullishSource{}))

	initialMark := contracts.MustDecimal("5000")
	if err := harness.venue.SetMarkPrice("BTC/USDT", initialMark); err != nil {
		t.Fatal(err)
	}
	initial, err := harness.venue.Place(context.Background(), contracts.OrderIntent{
		ClientID: "existing-profitable-long", Symbol: "BTC/USDT", Venue: "paper",
		Side: contracts.SideLong, Instrument: contracts.InstrumentSpot,
		OrderType: contracts.EntryTypeMarket, SizeQuote: contracts.MustDecimal("100"),
		Price: &initialMark, Leverage: 1, MarginMode: contracts.MarginModeIsolated,
	})
	if err != nil || initial.Status != contracts.OrderStatusFilled {
		t.Fatalf("initial position: %+v, %v", initial, err)
	}
	before, _ := harness.venue.Positions(context.Background())
	result := harness.service.RunOnce(context.Background(), "run-auto-add", "BTC/USDT")
	if result.Status != contracts.RunExecuted || result.Proposal == nil || result.Proposal.AddOnPlan == nil ||
		result.Decision == nil || result.Decision.Operator != "auto-policy" {
		t.Fatalf("automatic add status=%s before=%+v proposal=%+v decision=%+v error=%v", result.Status, before, result.Proposal, result.Decision, result.Error)
	}
	after, err := harness.venue.Positions(context.Background())
	if err != nil || len(before) != 1 || len(after) != 1 || after[0].SizeBase.Cmp(before[0].SizeBase) <= 0 {
		t.Fatalf("same-side position was not increased: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestRunOnceAutoApprovalUsesFinalRiskScore(t *testing.T) {
	harness := newHarness(t, func(settings *config.Settings) {
		settings.Automation.Enabled = true
		settings.Automation.ApprovalEnabled = true
		settings.Automation.MinConfidence = 0
		settings.Automation.MinRewardRisk = 1
		settings.Automation.MinEntryQuote = contracts.MustDecimal("1")
		settings.AutoSymbols = "BTC/USDT"
		settings.Automation.MaxRiskScore = 0
		settings.Automation.MaxQuote = contracts.MustDecimal("200")
	}, orchestrator.WithDataSource(bullishSource{}))

	result := harness.service.RunOnce(context.Background(), "run-auto-risk", "BTC/USDT")
	if result.Status != contracts.RunRejected || result.Decision == nil ||
		result.Decision.Operator != "auto-policy" || result.Assessment == nil ||
		!strings.Contains(strings.Join(result.Assessment.HardViolations, "\n"), "auto_risk_score:") {
		t.Fatalf("final automatic risk gate did not reject: %+v", result)
	}
	positions, err := harness.venue.Positions(context.Background())
	if err != nil || len(positions) != 0 {
		t.Fatalf("final risk rejection opened a position: %+v, %v", positions, err)
	}
}

func TestRunOnceReversesOnlyAfterConfirmationAndFlatVerification(t *testing.T) {
	tracker, err := riskstate.New(context.Background(), nil, contracts.MustDecimal("10000"))
	if err != nil {
		t.Fatal(err)
	}
	harness := newHarness(t, func(settings *config.Settings) {
		settings.Automation.Enabled = true
		settings.Automation.EntryEnabled = true
		settings.Automation.ApprovalEnabled = true
		settings.Automation.ReverseEnabled = true
		settings.Automation.MinConfidence = 0
		settings.Automation.MinRewardRisk = 1
		settings.Automation.ReverseMinConfidence = 0
		settings.Automation.ReverseMinRewardRisk = 1
		settings.Automation.ReverseConfirmations = 2
		settings.Automation.ReverseCooldownMins = 0
		settings.Automation.MaxReversalsPerDay = 5
		settings.Automation.MinEntryQuote = contracts.MustDecimal("1")
		settings.AutoSymbols = "BTC/USDT"
		settings.Automation.MaxRiskScore = 1
		settings.Automation.MaxQuote = contracts.MustDecimal("1000")
	}, orchestrator.WithDataSource(bullishSource{}), orchestrator.WithRiskState(tracker))

	mark := contracts.MustDecimal("6341")
	if err := harness.venue.SetMarkPrice("BTC/USDT", mark); err != nil {
		t.Fatal(err)
	}
	initial := contracts.OrderIntent{
		ClientID: "initial-short", Symbol: "BTC/USDT", Venue: "paper", Side: contracts.SideShort,
		Instrument: contracts.InstrumentSpot, OrderType: contracts.EntryTypeMarket,
		SizeQuote: contracts.MustDecimal("100"), Price: &mark, Leverage: 1,
		MarginMode: contracts.MarginModeIsolated,
	}
	execution, err := harness.venue.Place(context.Background(), initial)
	if err != nil || execution.Status != contracts.OrderStatusFilled {
		t.Fatalf("initial execution=%+v err=%v", execution, err)
	}
	initialProposal := contracts.TradeProposal{
		Symbol: initial.Symbol, Venue: initial.Venue, Side: initial.Side, Instrument: initial.Instrument,
		SizeQuote: initial.SizeQuote, Leverage: initial.Leverage, MarginMode: initial.MarginMode,
	}
	if err := tracker.RecordOpen(context.Background(), "initial-run", initialProposal, execution, contracts.MustDecimal("10000")); err != nil {
		t.Fatal(err)
	}

	first := harness.service.RunOnce(context.Background(), "reverse-one", "BTC/USDT")
	if first.Status != contracts.RunNotApproved {
		t.Fatalf("first status=%s error=%v decision=%+v", first.Status, first.Error, first.Decision)
	}
	positions, _ := harness.venue.Positions(context.Background())
	if len(positions) != 1 || positions[0].Side != contracts.SideShort {
		t.Fatalf("first confirmation changed position: %+v", positions)
	}

	second := harness.service.RunOnce(context.Background(), "reverse-two", "BTC/USDT")
	if second.Status != contracts.RunExecuted {
		t.Fatalf("second status=%s error=%v assessment=%+v", second.Status, second.Error, second.Assessment)
	}
	positions, _ = harness.venue.Positions(context.Background())
	if len(positions) != 1 || positions[0].Side != contracts.SideLong {
		t.Fatalf("position was not reversed: %+v", positions)
	}
	if _, ok := tracker.OpenTrade("BTC/USDT", contracts.InstrumentSpot); !ok {
		t.Fatal("new reverse position was not recorded as open")
	}
	trades := tracker.Trades()
	if len(trades) != 3 || trades[1].Kind != "close" || trades[1].ClientID != "reverse-close-reverse-two" || trades[2].Side != contracts.SideLong {
		t.Fatalf("unexpected reversal trade ledger: %+v", trades)
	}
}

func TestRunOnceAutoPolicyMismatchFallsBackToGate(t *testing.T) {
	harness := newHarness(t, func(settings *config.Settings) {
		settings.Automation.Enabled = true
		settings.Automation.ApprovalEnabled = true
		settings.Automation.MinConfidence = 0
		settings.Automation.MinRewardRisk = 1
		settings.AutoSymbols = "ETH/USDT" // symbol mismatch disables the auto policy
		settings.Automation.MaxQuote = contracts.MustDecimal("10000")
	}, orchestrator.WithDataSource(bullishSource{}))

	go resolveWhenPending(t, harness.gate, "run-gate", contracts.ApprovalRequest{Decision: contracts.ApprovalApprove})
	result := harness.service.RunOnce(context.Background(), "run-gate", "BTC/USDT")
	if result.Status != contracts.RunExecuted {
		t.Fatalf("status = %s, error = %v", result.Status, result.Error)
	}
	if result.Decision == nil || result.Decision.Operator == "auto-policy" {
		t.Fatalf("expected human decision, got %+v", result.Decision)
	}
}

func TestRunOnceKillSwitchRejectsBeforeExecution(t *testing.T) {
	harness := newHarness(t, func(settings *config.Settings) {
		settings.Kill = true
	}, orchestrator.WithDataSource(bullishSource{}))

	result := harness.service.RunOnce(context.Background(), "run-kill", "BTC/USDT")
	if result.Status != contracts.RunRejected {
		t.Fatalf("status = %s, error = %v", result.Status, result.Error)
	}
	if result.Assessment == nil || result.Assessment.Verdict != contracts.VerdictRejected {
		t.Fatalf("expected rejected assessment, got %+v", result.Assessment)
	}
	if result.Execution != nil {
		t.Fatalf("kill switch must prevent execution, got %+v", result.Execution)
	}
	positions, err := harness.venue.Positions(context.Background())
	if err != nil || len(positions) != 0 {
		t.Fatalf("positions after kill-switch rejection = %v (err=%v)", positions, err)
	}
}

func TestRunOnceHumanRejectIsNotApproved(t *testing.T) {
	harness := newHarness(t, nil, orchestrator.WithDataSource(bullishSource{}))
	go resolveWhenPending(t, harness.gate, "run-reject", contracts.ApprovalRequest{Decision: contracts.ApprovalReject})
	result := harness.service.RunOnce(context.Background(), "run-reject", "BTC/USDT")
	if result.Status != contracts.RunNotApproved {
		t.Fatalf("status = %s, error = %v", result.Status, result.Error)
	}
	if result.Decision == nil || result.Decision.Decision != contracts.ApprovalReject {
		t.Fatalf("expected reject decision, got %+v", result.Decision)
	}
	if result.Execution != nil {
		t.Fatal("rejected run must not execute")
	}
}

func TestRunOnceApprovalTimeoutFailsSafe(t *testing.T) {
	harness := newHarness(t, func(settings *config.Settings) {
		settings.Risk.ApprovalTimeoutSeconds = 1
	}, orchestrator.WithDataSource(bullishSource{}))

	result := harness.service.RunOnce(context.Background(), "run-timeout", "BTC/USDT")
	if result.Status != contracts.RunNotApproved {
		t.Fatalf("status = %s, error = %v", result.Status, result.Error)
	}
	if result.Decision == nil || result.Decision.Decision != contracts.ApprovalReject {
		t.Fatalf("timeout must resolve as reject, got %+v", result.Decision)
	}
}

func TestRunOnceModifyDownsizesAndRevalidates(t *testing.T) {
	harness := newHarness(t, nil, orchestrator.WithDataSource(bullishSource{}))
	smaller := contracts.MustDecimal("50")
	go resolveWhenPending(t, harness.gate, "run-modify", contracts.ApprovalRequest{
		Decision: contracts.ApprovalModify, Size: &smaller,
	})
	result := harness.service.RunOnce(context.Background(), "run-modify", "BTC/USDT")
	if result.Status != contracts.RunExecuted {
		t.Fatalf("status = %s, error = %v", result.Status, result.Error)
	}
	if result.Proposal == nil || result.Proposal.SizeQuote.Cmp(smaller) != 0 {
		t.Fatalf("final proposal size = %+v, want 50", result.Proposal)
	}
	if result.Decision == nil || result.Decision.Modified == nil {
		t.Fatalf("modify decision must carry the modified proposal, got %+v", result.Decision)
	}
}

func TestRunOnceFallsBackWhenPrimarySnapshotFails(t *testing.T) {
	harness := newHarness(t, func(settings *config.Settings) {
		settings.Risk.ApprovalTimeoutSeconds = 1
	}, orchestrator.WithDataSource(failingSource{}))

	result := harness.service.RunOnce(context.Background(), "run-fallback", "BTC/USDT")
	if result.Status == contracts.RunError {
		t.Fatalf("fallback should absorb the primary failure, got error = %v", result.Error)
	}
}

func TestStartAsyncLifecycleAndClose(t *testing.T) {
	harness := newHarness(t, nil, orchestrator.WithDataSource(emptySource{}))
	accepted, err := harness.service.Start("BTC/USDT")
	if err != nil || accepted.RunID == "" {
		t.Fatalf("Start() = %+v, err = %v", accepted, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if result, ok := harness.service.GetRun(accepted.RunID); ok {
			if result.Status != contracts.RunNoTrade {
				t.Fatalf("async run status = %s, error = %v", result.Status, result.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("async run never recorded a result")
		}
		time.Sleep(5 * time.Millisecond)
	}

	harness.service.Close()
	if _, err := harness.service.Start("BTC/USDT"); !errors.Is(err, orchestrator.ErrStopped) {
		t.Fatalf("Start after Close error = %v, want ErrStopped", err)
	}
	if _, err := harness.service.Start(" "); !errors.Is(err, orchestrator.ErrEmptySymbol) {
		t.Fatalf("Start with blank symbol error = %v, want ErrEmptySymbol", err)
	}
}

func TestExecutedRunJournalsOrderLifecycle(t *testing.T) {
	harness := newHarness(t, func(settings *config.Settings) {
		settings.Automation.Enabled = true
		settings.Automation.ApprovalEnabled = true
		settings.Automation.MinConfidence = 0
		settings.Automation.MinRewardRisk = 1
		settings.AutoSymbols = "BTC/USDT"
		settings.Automation.MaxRiskScore = 1
		settings.Automation.MaxQuote = contracts.MustDecimal("10000")
	}, orchestrator.WithDataSource(bullishSource{}))

	result := harness.service.RunOnce(context.Background(), "run-journal", "BTC/USDT")
	if result.Status != contracts.RunExecuted {
		t.Fatalf("status = %s, error = %v", result.Status, result.Error)
	}
	order, ok := harness.service.Order("run-journal")
	if !ok {
		t.Fatal("executed run left no order in the journal")
	}
	wantStatus := contracts.OrderStatusFilled
	if len(result.Execution.ProtectiveOrders) > 0 {
		wantStatus = contracts.OrderStatusProtectivePlaced
	}
	if order.Status != wantStatus {
		t.Fatalf("journaled status = %s, want %s", order.Status, wantStatus)
	}
	if len(order.Events) < 3 {
		t.Fatalf("expected open/submit/result events, got %d", len(order.Events))
	}
	for _, unresolved := range harness.service.UnresolvedOrders() {
		if unresolved.ClientID == "run-journal" && wantStatus == contracts.OrderStatusFilled {
			// filled without protective orders stays unresolved by design
			return
		}
	}
}

// TestStartSerializesOnSharedSymbolLocks proves that a lock held by an
// external runtime caller (scanner, close, reconciliation) delays the
// orchestrator run for the same symbol until the lock is released.
func TestStartSerializesOnSharedSymbolLocks(t *testing.T) {
	locks := runtimecore.NewSymbolLocks()
	harness := newHarness(t, nil,
		orchestrator.WithDataSource(emptySource{}), orchestrator.WithSymbolLocks(locks))

	entered := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- locks.Do(context.Background(), "BTC/USDT", func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	accepted, err := harness.service.Start("BTC/USDT")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, ok := harness.service.GetRun(accepted.RunID); ok {
		t.Fatal("run completed while the shared symbol lock was still held")
	}

	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if result, ok := harness.service.GetRun(accepted.RunID); ok {
			if result.Status != contracts.RunNoTrade {
				t.Fatalf("run status = %s, error = %v", result.Status, result.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never completed after the lock was released")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
