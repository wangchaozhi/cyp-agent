package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/observability"
)

type RunFunc func(ctx context.Context, symbol string) error
type RuntimeStateProvider func() RuntimeState

type ScannerConfig struct {
	Symbols  []string
	Interval time.Duration
	Run      RunFunc
	State    RuntimeStateProvider
	Safety   *SafetyState
	Locks    *SymbolLocks
	Logger   *slog.Logger
	Metrics  *observability.RuntimeMetrics
	OnError  func(error)
}

type Scanner struct {
	symbols  []string
	interval time.Duration
	run      RunFunc
	state    RuntimeStateProvider
	safety   *SafetyState
	locks    *SymbolLocks
	logger   *slog.Logger
	metrics  *observability.RuntimeMetrics
	onError  func(error)
}

func NewScanner(config ScannerConfig) (*Scanner, error) {
	if config.Run == nil {
		return nil, errors.New("scanner run function is required")
	}
	if config.Interval <= 0 {
		return nil, errors.New("scanner interval must be positive")
	}
	symbols := uniqueSymbols(config.Symbols)
	if len(symbols) == 0 {
		return nil, errors.New("scanner requires at least one symbol")
	}
	if config.State == nil {
		config.State = func() RuntimeState {
			return RuntimeState{Mode: "paper", ExecutionVenue: "paper"}
		}
	}
	if config.Safety == nil {
		config.Safety = NewSafetyState()
	}
	if config.Locks == nil {
		config.Locks = NewSymbolLocks()
	}
	if config.Logger == nil {
		config.Logger = observability.DefaultLogger("scanner")
	}
	return &Scanner{
		symbols: symbols, interval: config.Interval, run: config.Run, state: config.State,
		safety: config.Safety, locks: config.Locks, logger: config.Logger,
		metrics: config.Metrics, onError: config.OnError,
	}, nil
}

func uniqueSymbols(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (scanner *Scanner) Symbols() []string {
	return append([]string(nil), scanner.symbols...)
}

func (scanner *Scanner) ScanOnce(ctx context.Context) error {
	if ctx == nil {
		return errors.New("scanner context is required")
	}
	errorsSeen := make([]error, 0)
	for _, symbol := range scanner.symbols {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := scanner.safety.CheckNewPosition(scanner.state()); err != nil {
			errorsSeen = append(errorsSeen, fmt.Errorf("scan %s: %w", symbol, err))
			continue
		}
		err := scanner.locks.Do(ctx, symbol, func(runContext context.Context) error {
			return scanner.run(runContext, symbol)
		})
		if err != nil {
			errorsSeen = append(errorsSeen, fmt.Errorf("scan %s: %w", symbol, err))
		}
	}
	joined := errors.Join(errorsSeen...)
	scanner.metrics.RecordScan(joined)
	return joined
}

func (scanner *Scanner) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("scanner context is required")
	}
	for {
		if err := scanner.ScanOnce(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			scanner.logger.ErrorContext(ctx, "scan_cycle_failed", "error", err.Error())
			if scanner.onError != nil {
				scanner.onError(err)
			}
		}
		timer := time.NewTimer(scanner.interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (scanner *Scanner) RunCycles(ctx context.Context, cycles int) error {
	if cycles < 0 {
		return errors.New("scan cycles cannot be negative")
	}
	errorsSeen := make([]error, 0)
	for cycle := 0; cycle < cycles; cycle++ {
		if err := scanner.ScanOnce(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			errorsSeen = append(errorsSeen, err)
		}
	}
	return errors.Join(errorsSeen...)
}
