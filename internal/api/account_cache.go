package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

const accountSnapshotTTL = time.Second

// accountSnapshot is a short-lived, read-only view shared by the positions,
// risk, and portfolio endpoints. Those endpoints are polled together by the
// dashboard, so fetching the same private account data independently would
// multiply exchange traffic without making the view meaningfully fresher.
type accountSnapshot struct {
	balances  contracts.Balances
	positions []contracts.Position
	marks     map[string]contracts.Decimal
}

type accountSnapshotCache struct {
	mu        sync.Mutex
	ttl       time.Duration
	expiresAt time.Time
	value     accountSnapshot
}

func newAccountSnapshotCache(ttl time.Duration) *accountSnapshotCache {
	if ttl <= 0 {
		ttl = accountSnapshotTTL
	}
	return &accountSnapshotCache{ttl: ttl}
}

func (cache *accountSnapshotCache) Load(ctx context.Context, target venue.Venue) (accountSnapshot, error) {
	if cache == nil || target == nil {
		return accountSnapshot{}, errors.New("account snapshot dependencies are unavailable")
	}
	if ctx == nil {
		return accountSnapshot{}, errors.New("account snapshot context is required")
	}
	if err := ctx.Err(); err != nil {
		return accountSnapshot{}, err
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if time.Now().Before(cache.expiresAt) {
		return cloneAccountSnapshot(cache.value), nil
	}

	value, err := fetchAccountSnapshot(ctx, target)
	if err != nil {
		return accountSnapshot{}, err
	}
	cache.value = cloneAccountSnapshot(value)
	cache.expiresAt = time.Now().Add(cache.ttl)
	return cloneAccountSnapshot(value), nil
}

func (cache *accountSnapshotCache) Invalidate() {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	cache.expiresAt = time.Time{}
	cache.mu.Unlock()
}

func fetchAccountSnapshot(ctx context.Context, target venue.Venue) (accountSnapshot, error) {
	var value accountSnapshot
	var balancesErr, positionsErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		value.balances, balancesErr = target.Balances(ctx)
	}()
	go func() {
		defer wait.Done()
		value.positions, positionsErr = target.Positions(ctx)
	}()
	wait.Wait()
	if balancesErr != nil || positionsErr != nil {
		return accountSnapshot{}, errors.Join(balancesErr, positionsErr)
	}

	value.marks = make(map[string]contracts.Decimal, len(value.positions))
	var marksMu sync.Mutex
	wait = sync.WaitGroup{}
	seen := make(map[string]struct{}, len(value.positions))
	for _, position := range value.positions {
		if _, found := seen[position.Symbol]; found {
			continue
		}
		seen[position.Symbol] = struct{}{}
		symbol := position.Symbol
		wait.Add(1)
		go func() {
			defer wait.Done()
			mark, err := target.FetchTicker(ctx, symbol)
			if err != nil || !mark.IsPositive() {
				return
			}
			marksMu.Lock()
			value.marks[symbol] = mark
			marksMu.Unlock()
		}()
	}
	wait.Wait()
	return value, nil
}

func cloneAccountSnapshot(value accountSnapshot) accountSnapshot {
	cloned := value
	cloned.positions = append([]contracts.Position(nil), value.positions...)
	cloned.marks = make(map[string]contracts.Decimal, len(value.marks))
	for symbol, mark := range value.marks {
		cloned.marks[symbol] = mark
	}
	return cloned
}
