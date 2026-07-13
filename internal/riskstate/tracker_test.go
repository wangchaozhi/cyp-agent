package riskstate

import (
	"context"
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

func decimal(value string) *contracts.Decimal {
	parsed := contracts.MustDecimal(value)
	return &parsed
}
