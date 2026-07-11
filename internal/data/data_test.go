package data

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func flatCandles(count int, price string) []contracts.Candle {
	value := contracts.MustDecimal(price)
	result := make([]contracts.Candle, count)
	for index := range result {
		result[index] = contracts.Candle{
			TS: time.Unix(int64(index), 0).UTC(), Open: value, High: value,
			Low: value, Close: value, Volume: contracts.NewDecimalFromInt64(1),
		}
	}
	return result
}

func TestIndicatorsMatchBaselineShapes(t *testing.T) {
	values := make([]float64, 60)
	for index := range values {
		values[index] = 100 + float64(index)*0.5
	}
	if value, ok := SMA(values[:10], 10); !ok || value != 102.25 {
		t.Fatalf("SMA=%v,%v", value, ok)
	}
	if value, ok := RSI(values, 14); !ok || value != 100 {
		t.Fatalf("RSI=%v,%v", value, ok)
	}
	macd, ok := MACD(values, 12, 26, 9)
	if !ok || math.IsNaN(macd.Line) || math.IsNaN(macd.Signal) {
		t.Fatalf("MACD=%#v,%v", macd, ok)
	}
	bands, ok := Bollinger(values, 20, 2)
	if !ok || bands.Lower > bands.Mid || bands.Mid > bands.Upper {
		t.Fatalf("Bollinger=%#v,%v", bands, ok)
	}
	if value, ok := ATR(flatCandles(30, "100"), 14); !ok || value != 0 {
		t.Fatalf("ATR=%v,%v", value, ok)
	}
	if _, ok := ATR(flatCandles(3, "100"), 14); ok {
		t.Fatal("ATR unexpectedly accepted insufficient data")
	}
}

func TestIndicatorSnapshotAndVolatility(t *testing.T) {
	candles := make([]contracts.Candle, 80)
	for index := range candles {
		close := contracts.NewDecimalFromInt64(int64(100 + index))
		candles[index] = contracts.Candle{
			TS: time.Unix(int64(index), 0).UTC(), Open: close, High: close,
			Low: close, Close: close, Volume: contracts.NewDecimalFromInt64(1),
		}
	}
	snapshot := IndicatorSnapshot(candles)
	for _, name := range []string{"last_close", "sma_fast", "sma_slow", "rsi", "macd", "atr", "bb_lower"} {
		if snapshot[name] == nil {
			t.Fatalf("indicator %s is nil", name)
		}
	}
	calm := []float64{0.001, -0.001, 0.001, -0.001, 0.001}
	wild := []float64{0.05, -0.05, 0.05, -0.05, 0.05}
	if EWMAVolatility(wild) <= EWMAVolatility(calm) {
		t.Fatal("EWMA did not react to larger returns")
	}
	spike := append(make([]float64, 40), 0.08, -0.08, 0.08, -0.08)
	for index := 0; index < 40; index++ {
		spike[index] = 0.001
	}
	if EWMAVolatility(spike) <= RealizedVolatility(spike) {
		t.Fatal("EWMA did not overweight the recent spike")
	}
	if got := len(SimpleReturns(candles)); got != len(candles)-1 {
		t.Fatalf("returns=%d", got)
	}
}

func TestSyntheticIsDeterministicAndLiveTicksMove(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC) }
	stable := NewSyntheticMarketData(WithSyntheticClock(clock))
	first, err := stable.Snapshot(context.Background(), "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	second, err := stable.Snapshot(context.Background(), "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.OHLCV) != 200 || len(second.OHLCV) != 200 {
		t.Fatalf("unexpected candle count %d/%d", len(first.OHLCV), len(second.OHLCV))
	}
	for index := range first.OHLCV {
		if first.OHLCV[index].Close.Cmp(second.OHLCV[index].Close) != 0 {
			t.Fatalf("deterministic close changed at %d", index)
		}
	}
	if first.Derivatives == nil || first.Sentiment == nil {
		t.Fatal("synthetic dimensions are missing")
	}

	live := NewSyntheticMarketData(WithSyntheticClock(clock), WithLiveTicks(true))
	liveFirst, _ := live.Snapshot(context.Background(), "BTC/USDT")
	liveSecond, _ := live.Snapshot(context.Background(), "BTC/USDT")
	if liveFirst.OHLCV[len(liveFirst.OHLCV)-1].Close.Cmp(
		liveSecond.OHLCV[len(liveSecond.OHLCV)-1].Close,
	) == 0 {
		t.Fatal("live tick did not advance")
	}
}

type fakeSnapshotVenue struct {
	fundingErr error
	oiErr      error
	lsrErr     error
	started    atomic.Int32
	release    chan struct{}
}

func (*fakeSnapshotVenue) ID() string { return "fakecex" }

func (venue *fakeSnapshotVenue) rendezvous(ctx context.Context) error {
	if venue.release == nil {
		return nil
	}
	if venue.started.Add(1) == 2 {
		close(venue.release)
	}
	select {
	case <-venue.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (venue *fakeSnapshotVenue) FetchOHLCV(
	ctx context.Context,
	_ string,
	_ string,
	limit int,
) ([]contracts.Candle, error) {
	if err := venue.rendezvous(ctx); err != nil {
		return nil, err
	}
	return flatCandles(limit, "100"), nil
}

func (venue *fakeSnapshotVenue) FetchOrderBook(
	ctx context.Context,
	_ string,
	_ int,
) (contracts.OrderBook, error) {
	if err := venue.rendezvous(ctx); err != nil {
		return contracts.OrderBook{}, err
	}
	return contracts.OrderBook{
		Bids: contracts.List[contracts.PriceLevel]{{contracts.MustDecimal("99"), contracts.MustDecimal("1")}},
		Asks: contracts.List[contracts.PriceLevel]{{contracts.MustDecimal("101"), contracts.MustDecimal("1")}},
	}, nil
}

func (venue *fakeSnapshotVenue) FetchFundingRate(context.Context, string) (contracts.Decimal, error) {
	return contracts.MustDecimal("0.0004"), venue.fundingErr
}

func (venue *fakeSnapshotVenue) FetchOpenInterest(context.Context, string) (contracts.Decimal, error) {
	return contracts.MustDecimal("1000000"), venue.oiErr
}

func (venue *fakeSnapshotVenue) FetchLongShortRatio(context.Context, string) (contracts.Decimal, error) {
	return contracts.MustDecimal("1.2"), venue.lsrErr
}

func TestCEXSnapshotConcurrencyAndDerivativeDegradation(t *testing.T) {
	venue := &fakeSnapshotVenue{release: make(chan struct{})}
	snapshot, err := NewCEXMarketData(venue).Snapshot(context.Background(), "BTC/USDT:USDT")
	if err != nil {
		t.Fatal(err)
	}
	if venue.started.Load() != 2 || len(snapshot.OHLCV) != 200 || snapshot.OrderBook == nil {
		t.Fatalf("independent dimensions did not load concurrently: %#v", snapshot)
	}
	if snapshot.Derivatives == nil || snapshot.Derivatives.FundingRate == nil ||
		snapshot.Derivatives.OpenInterest == nil || snapshot.Derivatives.LongShortRatio == nil {
		t.Fatalf("missing derivative dimensions: %#v", snapshot.Derivatives)
	}

	venue.fundingErr = errors.New("funding unavailable")
	snapshot, err = NewCEXMarketData(venue).Snapshot(context.Background(), "ETH/USDT:USDT")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Derivatives != nil || len(snapshot.OHLCV) != 200 {
		t.Fatal("funding failure did not degrade only derivatives")
	}

	spot, err := NewCEXMarketData(venue).Snapshot(context.Background(), "BTC/USDT")
	if err != nil || spot.Derivatives != nil {
		t.Fatalf("spot derivative behavior mismatch: %#v, %v", spot.Derivatives, err)
	}
}

func TestOnchainDataSourceDegrades(t *testing.T) {
	if got := NewOnchainDataSource(nil).Fetch(context.Background(), "ETH/USDC"); got != nil {
		t.Fatalf("unconfigured source returned %#v", got)
	}
	failing := NewOnchainDataSource(func(context.Context, string) (*contracts.OnchainData, error) {
		return nil, errors.New("offline")
	})
	if got := failing.Fetch(context.Background(), "ETH/USDC"); got != nil {
		t.Fatalf("failing source returned %#v", got)
	}
	flow := contracts.MustDecimal("120000")
	configured := NewOnchainDataSource(func(context.Context, string) (*contracts.OnchainData, error) {
		return &contracts.OnchainData{SmartMoneyFlow: &flow}, nil
	})
	if got := configured.Fetch(context.Background(), "ETH/USDC"); got == nil || got.SmartMoneyFlow.Cmp(flow) != 0 {
		t.Fatalf("configured source returned %#v", got)
	}
}
