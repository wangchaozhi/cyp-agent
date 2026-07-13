package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

type countingAccountVenue struct {
	venue.Venue
	balances  atomic.Int32
	positions atomic.Int32
	tickers   atomic.Int32
}

func (target *countingAccountVenue) Balances(context.Context) (contracts.Balances, error) {
	target.balances.Add(1)
	return contracts.Balances{QuoteCCY: "USDT", TotalQuote: contracts.MustDecimal("10000")}, nil
}

func (target *countingAccountVenue) Positions(context.Context) ([]contracts.Position, error) {
	target.positions.Add(1)
	return []contracts.Position{{
		Symbol: "BTC/USDT", Venue: "paper", Side: contracts.SideLong,
		Instrument: contracts.InstrumentSpot, SizeBase: contracts.MustDecimal("0.1"),
		EntryPrice: contracts.MustDecimal("60000"), Leverage: 1,
	}}, nil
}

func (target *countingAccountVenue) FetchTicker(context.Context, string) (contracts.Decimal, error) {
	target.tickers.Add(1)
	return contracts.MustDecimal("61000"), nil
}

func TestAccountSnapshotCacheCoalescesDashboardRequests(t *testing.T) {
	target := &countingAccountVenue{Venue: venue.NewPaperVenue()}
	cache := newAccountSnapshotCache(time.Minute)

	var wait sync.WaitGroup
	for range 12 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := cache.Load(context.Background(), target)
			if err != nil || len(value.positions) != 1 || !value.marks["BTC/USDT"].IsPositive() {
				t.Errorf("Load() = %+v, err = %v", value, err)
			}
		}()
	}
	wait.Wait()
	if target.balances.Load() != 1 || target.positions.Load() != 1 || target.tickers.Load() != 1 {
		t.Fatalf("upstream calls balances=%d positions=%d tickers=%d, want one each",
			target.balances.Load(), target.positions.Load(), target.tickers.Load())
	}

	cache.Invalidate()
	if _, err := cache.Load(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if target.balances.Load() != 2 || target.positions.Load() != 2 || target.tickers.Load() != 2 {
		t.Fatal("Invalidate() did not force a fresh account snapshot")
	}
}
