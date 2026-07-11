package venue

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func openIntent(clientID string) contracts.OrderIntent {
	stop := contracts.MustDecimal("90")
	return contracts.OrderIntent{
		ClientID:   clientID,
		Symbol:     "BTC/USDT",
		Venue:      "paper",
		Side:       contracts.SideLong,
		Instrument: contracts.InstrumentSpot,
		OrderType:  contracts.EntryTypeMarket,
		SizeQuote:  contracts.MustDecimal("1000"),
		Leverage:   1,
		MarginMode: contracts.MarginModeIsolated,
		StopLoss:   &stop,
	}
}

func mustSetMark(t *testing.T, venue *PaperVenue, text string) {
	t.Helper()
	if err := venue.SetMarkPrice("BTC/USDT", contracts.MustDecimal(text)); err != nil {
		t.Fatal(err)
	}
}

func TestPaperPreflightAdverseSlippageAndNoMark(t *testing.T) {
	venue := NewPaperVenue()
	report, err := venue.Preflight(context.Background(), openIntent("no-mark"))
	if err != nil {
		t.Fatal(err)
	}
	if report.OK || len(report.Reasons) != 1 {
		t.Fatalf("unexpected missing-mark report: %#v", report)
	}

	mustSetMark(t, venue, "100")
	report, err = venue.Preflight(context.Background(), openIntent("long"))
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.EstPrice.Cmp(contracts.MustDecimal("100.05")) != 0 {
		t.Fatalf("long preflight price=%s, want 100.05", report.EstPrice)
	}
	short := openIntent("short")
	short.Side = contracts.SideShort
	report, err = venue.Preflight(context.Background(), short)
	if err != nil {
		t.Fatal(err)
	}
	if report.EstPrice.Cmp(contracts.MustDecimal("99.95")) != 0 {
		t.Fatalf("short preflight price=%s, want 99.95", report.EstPrice)
	}
}

func TestPaperOpenCreatesPositionProtectionAndExactBalance(t *testing.T) {
	venue := NewPaperVenue()
	mustSetMark(t, venue, "100")
	intent := openIntent("open")
	intent.TakeProfit = contracts.List[contracts.Decimal]{contracts.MustDecimal("120")}
	result, err := venue.Place(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != contracts.OrderStatusFilled || len(result.ProtectiveOrders) != 2 {
		t.Fatalf("unexpected execution: %#v", result)
	}
	positions, err := venue.Positions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 || positions[0].Side != contracts.SideLong {
		t.Fatalf("unexpected positions: %#v", positions)
	}
	balances, err := venue.Balances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if balances.FreeQuote.Cmp(contracts.MustDecimal("8999.6")) != 0 {
		t.Fatalf("free quote=%s, want 8999.6", balances.FreeQuote)
	}
	if balances.TotalQuote.Cmp(contracts.MustDecimal("9999.1002498750624687656171914")) != 0 {
		t.Fatalf("total quote=%s did not preserve exact decimal accounting", balances.TotalQuote)
	}
}

func TestPaperPlaceIsIdempotentUnderConcurrency(t *testing.T) {
	venue := NewPaperVenue()
	mustSetMark(t, venue, "100")
	const workers = 64
	results := make(chan contracts.ExecutionResult, workers)
	errors := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := venue.Place(context.Background(), openIntent("duplicate"))
			results <- result
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	var orderID string
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if result.Status != contracts.OrderStatusFilled || result.OrderID == nil {
			t.Fatalf("unexpected replay result: %#v", result)
		}
		if orderID == "" {
			orderID = *result.OrderID
		} else if orderID != *result.OrderID {
			t.Fatalf("idempotent call returned %q and %q", orderID, *result.OrderID)
		}
	}
	positions, _ := venue.Positions(context.Background())
	if len(positions) != 1 {
		t.Fatalf("created %d positions, want 1", len(positions))
	}
	balances, _ := venue.Balances(context.Background())
	if balances.FreeQuote.Cmp(contracts.MustDecimal("8999.6")) != 0 {
		t.Fatalf("duplicate calls debited balance more than once: %s", balances.FreeQuote)
	}
}

func TestPaperRejectsInsufficientBalanceIdempotently(t *testing.T) {
	venue := NewPaperVenue(WithInitialQuote(contracts.MustDecimal("100")))
	mustSetMark(t, venue, "100")
	first, err := venue.Place(context.Background(), openIntent("too-large"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := venue.Place(context.Background(), openIntent("too-large"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != contracts.OrderStatusRejected || first.Error == nil ||
		!strings.Contains(*first.Error, "可用余额不足") {
		t.Fatalf("unexpected rejection: %#v", first)
	}
	if second.Error == nil || *second.Error != *first.Error {
		t.Fatalf("rejected idempotent replay changed: %#v / %#v", first, second)
	}
	balances, _ := venue.Balances(context.Background())
	if balances.FreeQuote.Cmp(contracts.MustDecimal("100")) != 0 {
		t.Fatalf("rejected order changed balance: %s", balances.FreeQuote)
	}
}

func TestPaperManualCloseRealizesPnLAndChargesFee(t *testing.T) {
	venue := NewPaperVenue()
	mustSetMark(t, venue, "100")
	if result, err := venue.Place(context.Background(), openIntent("open")); err != nil || result.Status != contracts.OrderStatusFilled {
		t.Fatalf("open result=%#v err=%v", result, err)
	}
	mustSetMark(t, venue, "110")
	closeIntent := contracts.OrderIntent{
		ClientID:   "close",
		Symbol:     "BTC/USDT",
		Side:       contracts.SideLong,
		Instrument: contracts.InstrumentSpot,
		Leverage:   1,
		ReduceOnly: true,
	}
	result, err := venue.Place(context.Background(), closeIntent)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != contracts.OrderStatusFilled || result.FeeQuote.IsZero() {
		t.Fatalf("unexpected close result: %#v", result)
	}
	positions, _ := venue.Positions(context.Background())
	if len(positions) != 0 {
		t.Fatalf("position remained after close: %#v", positions)
	}
	balances, _ := venue.Balances(context.Background())
	if balances.FreeQuote.Cmp(contracts.MustDecimal("10098")) <= 0 {
		t.Fatalf("profitable close was not realized: %s", balances.FreeQuote)
	}
}

func TestPaperProtectiveOrderAutoCloses(t *testing.T) {
	venue := NewPaperVenue()
	mustSetMark(t, venue, "100")
	intent := openIntent("protected")
	intent.TakeProfit = contracts.List[contracts.Decimal]{contracts.MustDecimal("105")}
	result, err := venue.Place(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	var takeProfit contracts.ProtectiveOrder
	for _, order := range result.ProtectiveOrders {
		if order.Kind == "take_profit" {
			takeProfit = order
		}
	}
	if takeProfit.OrderID == "" {
		t.Fatal("missing take-profit order")
	}
	mustSetMark(t, venue, "106")
	positions, _ := venue.Positions(context.Background())
	if len(positions) != 0 {
		t.Fatalf("take profit did not close position: %#v", positions)
	}
	trigger, ok := venue.Fill(takeProfit.OrderID + "-trigger")
	if !ok || trigger.Status != contracts.OrderStatusFilled || trigger.AvgPrice == nil ||
		trigger.AvgPrice.Cmp(contracts.MustDecimal("106")) != 0 {
		t.Fatalf("missing protective trigger fill: %#v, %v", trigger, ok)
	}
}

func TestPaperPerpMarginAndLiquidation(t *testing.T) {
	venue := NewPaperVenue()
	mustSetMark(t, venue, "100")
	intent := openIntent("perp")
	intent.Instrument = contracts.InstrumentPerp
	intent.Leverage = 5
	result, err := venue.Place(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != contracts.OrderStatusFilled {
		t.Fatalf("unexpected result: %#v", result)
	}
	balances, _ := venue.Balances(context.Background())
	if balances.FreeQuote.Cmp(contracts.MustDecimal("9799.6")) != 0 {
		t.Fatalf("perp free quote=%s, want 9799.6", balances.FreeQuote)
	}
	positions, _ := venue.Positions(context.Background())
	if len(positions) != 1 || positions[0].LiqPrice == nil ||
		positions[0].LiqPrice.Cmp(contracts.MustDecimal("80.040")) != 0 {
		t.Fatalf("unexpected perp position: %#v", positions)
	}
}

func TestPaperHonorsCanceledContext(t *testing.T) {
	venue := NewPaperVenue()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := venue.Place(ctx, openIntent("canceled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Place error=%v, want context.Canceled", err)
	}
	if _, err := venue.Positions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Positions error=%v, want context.Canceled", err)
	}
}

func TestPaperReturnsDefensiveCopies(t *testing.T) {
	venue := NewPaperVenue()
	mustSetMark(t, venue, "100")
	intent := openIntent("copy")
	intent.Instrument = contracts.InstrumentPerp
	intent.Leverage = 2
	result, err := venue.Place(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	result.ProtectiveOrders[0].Kind = "tampered"
	replayed, err := venue.Place(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ProtectiveOrders[0].Kind != "stop_loss" {
		t.Fatal("caller mutated stored idempotent result")
	}
	positions, _ := venue.Positions(context.Background())
	if len(positions) != 1 || positions[0].LiqPrice == nil {
		t.Fatalf("unexpected positions: %#v", positions)
	}
	*positions[0].LiqPrice = contracts.MustDecimal("1")
	again, _ := venue.Positions(context.Background())
	if again[0].LiqPrice.Cmp(contracts.MustDecimal("50.025")) != 0 {
		t.Fatal("caller mutated stored position through returned pointer")
	}
}
