package ohlcv

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/observability"
)

type BackfillerConfig struct {
	Archive   Archive
	Market    HistoricalVenue
	Symbols   func() []string
	Timeframe string
	Retention time.Duration
	Interval  time.Duration
	Logger    *slog.Logger
	Metrics   *observability.RuntimeMetrics
	Now       func() time.Time
}

type Backfiller struct {
	archive   Archive
	market    HistoricalVenue
	symbols   func() []string
	timeframe string
	retention time.Duration
	interval  time.Duration
	logger    *slog.Logger
	metrics   *observability.RuntimeMetrics
	now       func() time.Time
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

func NewBackfiller(config BackfillerConfig) (*Backfiller, error) {
	if config.Archive == nil || config.Market == nil || config.Symbols == nil {
		return nil, errors.New("OHLCV backfiller requires archive, market, and symbols")
	}
	if config.Retention <= 0 {
		return nil, errors.New("OHLCV backfill retention must be positive")
	}
	if config.Interval <= 0 {
		config.Interval = 6 * time.Hour
	}
	if strings.TrimSpace(config.Timeframe) == "" {
		config.Timeframe = "1h"
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	backfiller := &Backfiller{
		archive: config.Archive, market: config.Market, symbols: config.Symbols,
		timeframe: config.Timeframe, retention: config.Retention, interval: config.Interval,
		logger: config.Logger, metrics: config.Metrics, now: config.Now,
		cancel: cancel, done: make(chan struct{}),
	}
	go backfiller.run(ctx)
	return backfiller, nil
}

func (backfiller *Backfiller) run(ctx context.Context) {
	defer close(backfiller.done)
	backfiller.repair(ctx)
	ticker := time.NewTicker(backfiller.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			backfiller.repair(ctx)
		}
	}
}

func (backfiller *Backfiller) repair(ctx context.Context) {
	backfiller.metrics.RecordOHLCVRepairRun()
	for _, symbol := range backfiller.symbols() {
		if err := ctx.Err(); err != nil {
			return
		}
		repaired, err := backfiller.archive.Repair(ctx, backfiller.market, symbol,
			backfiller.timeframe, backfiller.retention, backfiller.now())
		if err != nil {
			backfiller.metrics.RecordOHLCVError()
			backfiller.logger.ErrorContext(ctx, "ohlcv_backfill_failed", "error", err.Error(),
				"venue", backfiller.market.ID(), "symbol", symbol, "timeframe", backfiller.timeframe)
			continue
		}
		if repaired > 0 {
			backfiller.metrics.RecordOHLCVBackfilled(uint64(repaired))
			backfiller.logger.InfoContext(ctx, "ohlcv_backfill_completed", "candles", repaired,
				"venue", backfiller.market.ID(), "symbol", symbol, "timeframe", backfiller.timeframe)
		}
	}
}

func (backfiller *Backfiller) Close(ctx context.Context) error {
	if backfiller == nil {
		return nil
	}
	backfiller.closeOnce.Do(backfiller.cancel)
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-backfiller.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
