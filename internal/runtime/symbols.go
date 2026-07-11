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
	semaphore, err := locks.semaphoreFor(ctx, symbol, operation)
	if err != nil {
		return err
	}
	select {
	case semaphore <- struct{}{}:
		defer func() { <-semaphore }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return operation(ctx)
}

func (locks *SymbolLocks) semaphoreFor(
	ctx context.Context,
	symbol string,
	operation func(context.Context) error,
) (chan struct{}, error) {
	if ctx == nil {
		return nil, errors.New("symbol lock context is required")
	}
	if operation == nil {
		return nil, errors.New("symbol operation is required")
	}
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, errors.New("symbol is required")
	}
	if locks == nil {
		return nil, errors.New("symbol locks are unavailable")
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	semaphore := locks.locks[symbol]
	if semaphore == nil {
		semaphore = make(chan struct{}, 1)
		locks.locks[symbol] = semaphore
	}
	return semaphore, nil
}
