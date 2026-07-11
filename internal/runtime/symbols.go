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
	locks map[string]chan struct{}
}

func NewSymbolLocks() *SymbolLocks {
	return &SymbolLocks{locks: make(map[string]chan struct{})}
}

func (locks *SymbolLocks) Do(
	ctx context.Context,
	symbol string,
	operation func(context.Context) error,
) error {
	if ctx == nil {
		return errors.New("symbol lock context is required")
	}
	if operation == nil {
		return errors.New("symbol operation is required")
	}
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return errors.New("symbol is required")
	}
	if locks == nil {
		return errors.New("symbol locks are unavailable")
	}
	locks.mu.Lock()
	semaphore := locks.locks[symbol]
	if semaphore == nil {
		semaphore = make(chan struct{}, 1)
		locks.locks[symbol] = semaphore
	}
	locks.mu.Unlock()

	select {
	case semaphore <- struct{}{}:
		defer func() { <-semaphore }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return operation(ctx)
}
