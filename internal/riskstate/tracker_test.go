package riskstate

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/persistence"
)

func TestTrackerPersistsRiskStatisticsAndTradeLedger(t *testing.T) {
	ctx := context.Background()
	repository := persistence.NewMemoryRepository(20)
	tracker, err := New(ctx, repository, contracts.MustDecimal("10000"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	// New initializes period keys from the wall clock. Pin the initial state to
	// the same test clock so this test does not cross a day/week as time passes.
	tracker.state = newState(contracts.MustDecimal("10000"), now)

	if err := tracker.ObserveEquity(ctx, contracts.MustDecimal("9000")); err != nil {
		t.Fatal(err)
	}
	proposal := contracts.TradeProposal{
		Symbol: "BTC/USDT", Side: contracts.SideLong, Instrument: contracts.InstrumentSpot,
	}
	open := contracts.ExecutionResult{
		ClientID: "open-1", Status: contracts.OrderStatusFilled,
		FilledBase: contracts.MustDecimal("1"), AvgPrice: decimal("100"),
		FeeQuote: contracts.MustDecimal("4"),
	}
	if err := tracker.RecordOpen(ctx, "run-1", proposal, open, contracts.MustDecimal("8996")); err != nil {
		t.Fatal(err)
	}
	position := contracts.Position{
		Symbol: "BTC/USDT", Side: contracts.SideLong, Instrument: contracts.InstrumentSpot,
		SizeBase: contracts.MustDecimal("1"), EntryPrice: contracts.MustDecimal("100"),
	}
	closeResult := contracts.ExecutionResult{
		ClientID: "close-1", Status: contracts.OrderStatusFilled,
		FilledBase: contracts.MustDecimal("1"), AvgPrice: decimal("90"), FeeQuote: contracts.MustDecimal("1"),
	}
	if _, err := tracker.RecordClose(ctx, "manual", position, closeResult, contracts.MustDecimal("8985")); err != nil {
		t.Fatal(err)
	}

	snapshot := tracker.Snapshot(contracts.MustDecimal("8985"))
	if snapshot.DailyDrawdown.String() != "0.1015" || snapshot.WeeklyDrawdown.String() != "0.1015" ||
		snapshot.TotalDrawdown.String() != "0.1015" {
		t.Fatalf("unexpected drawdowns: %+v", snapshot)
	}
	if snapshot.OrdersLastHour != 2 || snapshot.ConsecutiveLosses != 1 || snapshot.RealizedPNL.String() != "-15" {
		t.Fatalf("unexpected risk counters: %+v", snapshot)
	}
	if trades := tracker.Trades(); len(trades) != 2 || trades[1].PNLQuote.String() != "-11" {
		t.Fatalf("unexpected trades: %+v", trades)
	}

	restored, err := New(ctx, repository, contracts.MustDecimal("10000"))
	if err != nil {
		t.Fatal(err)
	}
	restored.now = func() time.Time { return now }
	restoredSnapshot := restored.Snapshot(contracts.MustDecimal("8985"))
	if restoredSnapshot.OrdersLastHour != 2 || restoredSnapshot.ConsecutiveLosses != 1 || len(restored.Trades()) != 2 {
		t.Fatalf("risk state did not survive reload: %+v", restoredSnapshot)
	}
}

func TestOpenTradeStopsAtLatestClose(t *testing.T) {
	tracker, err := New(context.Background(), nil, contracts.MustDecimal("10000"))
	if err != nil {
		t.Fatal(err)
	}
	proposal := contracts.TradeProposal{Symbol: "BTC/USDT", Side: contracts.SideLong, Instrument: contracts.InstrumentSpot}
	price := contracts.MustDecimal("100")
	open := contracts.ExecutionResult{ClientID: "open", Status: contracts.OrderStatusFilled, AvgPrice: &price, FilledBase: contracts.MustDecimal("1")}
	if err := tracker.RecordOpen(context.Background(), "run", proposal, open, contracts.MustDecimal("10000")); err != nil {
		t.Fatal(err)
	}
	position := contracts.Position{Symbol: proposal.Symbol, Side: proposal.Side, Instrument: proposal.Instrument, EntryPrice: price, SizeBase: contracts.MustDecimal("1")}
	closeExecution := contracts.ExecutionResult{ClientID: "close", Status: contracts.OrderStatusFilled, AvgPrice: &price, FilledBase: contracts.MustDecimal("1")}
	if _, err := tracker.RecordClose(context.Background(), "run", position, closeExecution, contracts.MustDecimal("10000")); err != nil {
		t.Fatal(err)
	}
	if trade, ok := tracker.OpenTrade(proposal.Symbol, proposal.Instrument); ok {
		t.Fatalf("closed trade returned as open: %+v", trade)
	}
}

func TestTrackerPublishesCVARAfterEnoughEquitySamples(t *testing.T) {
	tracker, err := New(context.Background(), nil, contracts.MustDecimal("10000"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	equity := contracts.MustDecimal("10000")
	for index := 0; index < 21; index++ {
		equity = equity.Sub(contracts.NewDecimalFromInt64(int64(index + 1)))
		now = now.Add(time.Minute)
		if err := tracker.ObserveEquity(context.Background(), equity); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := tracker.Snapshot(equity)
	if snapshot.CVaRSamples < minCVaRSamples || snapshot.PortfolioCVARQuote == nil || !snapshot.PortfolioCVARQuote.IsPositive() {
		t.Fatalf("expected empirical CVaR, got %+v", snapshot)
	}
}

func TestTrackerScopesPaperAndOKXDemoBaselines(t *testing.T) {
	ctx := context.Background()
	repository := persistence.NewMemoryRepository(20)
	paper, err := NewScoped(ctx, repository, contracts.MustDecimal("10000"), "paper")
	if err != nil {
		t.Fatal(err)
	}
	if err := paper.ObserveEquity(ctx, contracts.MustDecimal("9000")); err != nil {
		t.Fatal(err)
	}
	demo, err := NewScoped(ctx, repository, contracts.MustDecimal("5000"), "demo:okx")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := demo.Snapshot(contracts.MustDecimal("5000")); !snapshot.TotalDrawdown.IsZero() {
		t.Fatalf("Demo inherited Paper drawdown: %+v", snapshot)
	}
	restoredPaper, err := NewScoped(ctx, repository, contracts.MustDecimal("10000"), "paper")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := restoredPaper.Snapshot(contracts.MustDecimal("9000")); snapshot.TotalDrawdown.String() != "0.1" {
		t.Fatalf("Paper baseline was not preserved: %+v", snapshot)
	}
}

func TestPaperScopeCanImportLegacyUnscopedCheckpoint(t *testing.T) {
	ctx := context.Background()
	repository := persistence.NewMemoryRepository(20)
	legacy, err := New(ctx, repository, contracts.MustDecimal("10000"))
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.ObserveEquity(ctx, contracts.MustDecimal("9500")); err != nil {
		t.Fatal(err)
	}
	paper, err := NewScoped(ctx, repository, contracts.MustDecimal("10000"), "paper")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := paper.Snapshot(contracts.MustDecimal("9500")); snapshot.TotalDrawdown.String() != "0.05" {
		t.Fatalf("legacy Paper checkpoint was not imported: %+v", snapshot)
	}
}

func TestReconcilePositionsRepairsMissingAndStaleOpenMarkers(t *testing.T) {
	tracker, err := New(context.Background(), nil, contracts.MustDecimal("10000"))
	if err != nil {
		t.Fatal(err)
	}
	position := contracts.Position{
		Symbol: "ETH/USDT:USDT", Side: contracts.SideLong, Instrument: contracts.InstrumentPerp,
		EntryPrice: contracts.MustDecimal("2000"), SizeBase: contracts.MustDecimal("0.1"),
	}
	if err := tracker.ReconcilePositions(context.Background(), []contracts.Position{position}); err != nil {
		t.Fatal(err)
	}
	if trade, ok := tracker.OpenTrade(position.Symbol, position.Instrument); !ok || trade.Kind != "reconcile_open" {
		t.Fatalf("missing reconciled open marker: %+v ok=%t", trade, ok)
	}
	if err := tracker.ReconcilePositions(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if trade, ok := tracker.OpenTrade(position.Symbol, position.Instrument); ok {
		t.Fatalf("stale open marker survived flat reconciliation: %+v", trade)
	}
	trades := tracker.Trades()
	if len(trades) != 2 || trades[0].Kind != "reconcile_open" || trades[1].Kind != "reconcile_close" {
		t.Fatalf("unexpected reconciliation ledger: %+v", trades)
	}
	if err := tracker.ReconcilePositions(context.Background(), nil); err != nil || len(tracker.Trades()) != 2 {
		t.Fatalf("flat reconciliation was not idempotent: trades=%+v err=%v", tracker.Trades(), err)
	}
}

func TestReconcilePositionsRepairsOfflineReversal(t *testing.T) {
	tracker, err := New(context.Background(), nil, contracts.MustDecimal("10000"))
	if err != nil {
		t.Fatal(err)
	}
	position := contracts.Position{
		Symbol: "BTC/USDT:USDT", Side: contracts.SideLong, Instrument: contracts.InstrumentPerp,
		EntryPrice: contracts.MustDecimal("60000"), SizeBase: contracts.MustDecimal("0.1"),
	}
	if err := tracker.ReconcilePositions(context.Background(), []contracts.Position{position}); err != nil {
		t.Fatal(err)
	}
	position.Side = contracts.SideShort
	if err := tracker.ReconcilePositions(context.Background(), []contracts.Position{position}); err != nil {
		t.Fatal(err)
	}
	trade, exists := tracker.OpenTrade(position.Symbol, position.Instrument)
	if !exists || trade.Side != contracts.SideShort {
		t.Fatalf("offline reversal was not reconciled: exists=%v trade=%+v", exists, trade)
	}
	position.SizeBase = contracts.MustDecimal("0.2")
	if err := tracker.ReconcilePositions(context.Background(), []contracts.Position{position}); err != nil {
		t.Fatal(err)
	}
	trade, exists = tracker.OpenTrade(position.Symbol, position.Instrument)
	if !exists || trade.SizeBase.Cmp(position.SizeBase) != 0 {
		t.Fatalf("offline size change was not reconciled: exists=%v trade=%+v", exists, trade)
	}
}

type failingRiskRepository struct{}

func (failingRiskRepository) SaveCheckpoint(context.Context, string, string, any) error {
	return errors.New("storage unavailable")
}
func (failingRiskRepository) LoadCheckpoints(context.Context, string) (map[string]json.RawMessage, error) {
	return map[string]json.RawMessage{}, nil
}

func TestRiskStateRollsBackMemoryWhenPersistenceFails(t *testing.T) {
	tracker, err := New(context.Background(), failingRiskRepository{}, contracts.MustDecimal("10000"))
	if err != nil {
		t.Fatal(err)
	}
	before := clonePersistedState(tracker.state)
	if err := tracker.ObserveEquity(context.Background(), contracts.MustDecimal("9000")); err == nil {
		t.Fatal("failed persistence unexpectedly succeeded")
	}
	if tracker.state.CurrentEquity.Cmp(before.CurrentEquity) != 0 || len(tracker.state.EquityPoints) != len(before.EquityPoints) {
		t.Fatalf("failed persistence polluted memory: before=%+v after=%+v", before, tracker.state)
	}
}

func decimal(value string) *contracts.Decimal {
	parsed := contracts.MustDecimal(value)
	return &parsed
}
