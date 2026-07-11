package data

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type fakeTickerVenue struct {
	id           string
	price        contracts.Decimal
	priceErr     error
	funding      contracts.Decimal
	fundingErr   error
	tickerCalls  atomic.Int32
	fundingCalls atomic.Int32
	barrier      *sync.WaitGroup
}

func (venue *fakeTickerVenue) ID() string { return venue.id }

func (venue *fakeTickerVenue) FetchTicker(context.Context, string) (contracts.Decimal, error) {
	venue.tickerCalls.Add(1)
	if venue.barrier != nil {
		venue.barrier.Done()
		venue.barrier.Wait()
	}
	return venue.price, venue.priceErr
}

func (venue *fakeTickerVenue) FetchFundingRate(context.Context, string) (contracts.Decimal, error) {
	venue.fundingCalls.Add(1)
	return venue.funding, venue.fundingErr
}

func TestAggregatorSkipsFailuresAndFindsBestSpread(t *testing.T) {
	venues := []TickerVenue{
		&fakeTickerVenue{id: "a", price: contracts.MustDecimal("100")},
		&fakeTickerVenue{id: "b", priceErr: errors.New("offline")},
		&fakeTickerVenue{id: "c", price: contracts.MustDecimal("102")},
	}
	aggregator := NewMarketAggregator(venues)
	tickers := aggregator.Tickers(context.Background(), "BTC/USDT")
	if len(tickers) != 2 || tickers["a"].Cmp(contracts.MustDecimal("100")) != 0 {
		t.Fatalf("tickers=%#v", tickers)
	}
	buy := aggregator.BestVenue(context.Background(), "BTC/USDT", contracts.SideLong)
	sell := aggregator.BestVenue(context.Background(), "BTC/USDT", contracts.SideShort)
	if buy.Venue == nil || *buy.Venue != "a" || sell.Venue == nil || *sell.Venue != "c" {
		t.Fatalf("best buy/sell=%#v/%#v", buy, sell)
	}
	spread := spreadBPS(tickers)
	if spread == nil || spread.Cmp(contracts.MustDecimal("200")) != 0 {
		t.Fatalf("spread=%v, want 200", spread)
	}
}

func TestAggregatorFetchesConcurrently(t *testing.T) {
	barrier := &sync.WaitGroup{}
	barrier.Add(2)
	aggregator := NewMarketAggregator([]TickerVenue{
		&fakeTickerVenue{id: "a", price: contracts.MustDecimal("100"), barrier: barrier},
		&fakeTickerVenue{id: "b", price: contracts.MustDecimal("101"), barrier: barrier},
	})
	if got := len(aggregator.Tickers(context.Background(), "BTC/USDT")); got != 2 {
		t.Fatalf("tickers=%d", got)
	}
}

func TestAggregatorSummaryFetchesDimensionsOnceAndHints(t *testing.T) {
	a := &fakeTickerVenue{
		id: "a", price: contracts.MustDecimal("100"), funding: contracts.MustDecimal("0.0001"),
	}
	b := &fakeTickerVenue{
		id: "b", price: contracts.MustDecimal("101"), funding: contracts.MustDecimal("0.0008"),
	}
	summary := NewMarketAggregator([]TickerVenue{a, b}).Summary(context.Background(), "BTC/USDT")
	if summary.BestBuy.Venue == nil || *summary.BestBuy.Venue != "a" ||
		summary.BestSell.Venue == nil || *summary.BestSell.Venue != "b" {
		t.Fatalf("unexpected best quotes: %#v", summary)
	}
	if summary.SpreadBPS == nil || summary.SpreadBPS.Cmp(contracts.MustDecimal("100")) != 0 {
		t.Fatalf("spread=%v", summary.SpreadBPS)
	}
	if len(summary.ArbHints) < 2 {
		t.Fatalf("expected price and funding hints: %#v", summary.ArbHints)
	}
	if a.tickerCalls.Load() != 1 || b.tickerCalls.Load() != 1 ||
		a.fundingCalls.Load() != 1 || b.fundingCalls.Load() != 1 {
		t.Fatalf("duplicate upstream calls: ticker=%d/%d funding=%d/%d",
			a.tickerCalls.Load(), b.tickerCalls.Load(), a.fundingCalls.Load(), b.fundingCalls.Load())
	}
}

type tickerOnlyVenue struct{ id string }

func (venue tickerOnlyVenue) ID() string { return venue.id }
func (tickerOnlyVenue) FetchTicker(context.Context, string) (contracts.Decimal, error) {
	return contracts.MustDecimal("100"), nil
}

func TestAggregatorSkipsUnsupportedFunding(t *testing.T) {
	a := &fakeTickerVenue{id: "a", price: contracts.MustDecimal("100"), funding: contracts.MustDecimal("0.0001")}
	rates := NewMarketAggregator([]TickerVenue{a, tickerOnlyVenue{id: "x"}}).
		FundingRates(context.Background(), "BTC/USDT")
	if len(rates) != 1 || rates["a"].Cmp(contracts.MustDecimal("0.0001")) != 0 {
		t.Fatalf("rates=%#v", rates)
	}
}
