package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// SymbolLocks serializes every action for one symbol while allowing unrelated
// symbols to run independently. Scanner, manual run, close, and reconciliation
// callers should share the same instance.
type SymbolLocks struct {
	mu    sync.Mutex
	locks map[string]*symbolLock
}

type symbolLock struct {
	semaphore  chan struct{}
	references int
}

func NewSymbolLocks() *SymbolLocks {
	return &SymbolLocks{locks: make(map[string]*symbolLock)}
}

func (locks *SymbolLocks) Do(
	ctx context.Context,
	symbol string,
	operation func(context.Context) error,
) error {
	entry, key, err := locks.entryFor(ctx, symbol, operation)
	if err != nil {
		return err
	}
	defer locks.release(key, entry)
	select {
	case entry.semaphore <- struct{}{}:
		defer func() { <-entry.semaphore }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return operation(ctx)
}

func (locks *SymbolLocks) entryFor(
	ctx context.Context,
	symbol string,
	operation func(context.Context) error,
) (*symbolLock, string, error) {
	if ctx == nil {
		return nil, "", errors.New("symbol lock context is required")
	}
	if operation == nil {
		return nil, "", errors.New("symbol operation is required")
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return nil, "", errors.New("symbol is required")
	}
	if locks == nil {
		return nil, "", errors.New("symbol locks are unavailable")
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	entry := locks.locks[symbol]
	if entry == nil {
		entry = &symbolLock{semaphore: make(chan struct{}, 1)}
		locks.locks[symbol] = entry
	}
	entry.references++
	return entry, symbol, nil
}

func (locks *SymbolLocks) release(symbol string, entry *symbolLock) {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	entry.references--
	if entry.references == 0 && locks.locks[symbol] == entry {
		delete(locks.locks, symbol)
	}
}
