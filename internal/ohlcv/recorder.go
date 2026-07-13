package ohlcv

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/observability"
)

type RecorderStore interface {
	Save(context.Context, string, string, string, []contracts.Candle) error
	Prune(context.Context, time.Time) (int64, error)
}

type RecorderConfig struct {
	Store           RecorderStore
	Retention       time.Duration
	CleanupInterval time.Duration
	QueueSize       int
	WriteTimeout    time.Duration
	Logger          *slog.Logger
	Metrics         *observability.RuntimeMetrics
	Now             func() time.Time
}

type recordBatch struct {
	venue, symbol, timeframe string
	candles                  []contracts.Candle
}

type AsyncRecorder struct {
	store        RecorderStore
	retention    time.Duration
	cleanupEvery time.Duration
	writeTimeout time.Duration
	logger       *slog.Logger
	metrics      *observability.RuntimeMetrics
	now          func() time.Time
	queue        chan recordBatch
	cancel       context.CancelFunc
	done         chan struct{}
	closing      atomic.Bool
	closeOnce    sync.Once
}

func NewAsyncRecorder(config RecorderConfig) (*AsyncRecorder, error) {
	if config.Store == nil {
		return nil, errors.New("OHLCV recorder store is required")
	}
	if config.Retention <= 0 {
		return nil, errors.New("OHLCV retention must be positive")
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 256
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = 24 * time.Hour
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = 10 * time.Second
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	recorder := &AsyncRecorder{
		store: config.Store, retention: config.Retention, cleanupEvery: config.CleanupInterval,
		writeTimeout: config.WriteTimeout, logger: config.Logger, metrics: config.Metrics,
		now: config.Now, queue: make(chan recordBatch, config.QueueSize), cancel: cancel,
		done: make(chan struct{}),
	}
	go recorder.run(ctx)
	return recorder, nil
}

// Record validates and queues only closed candles. It never waits for
// PostgreSQL; false means the bounded queue was full or the recorder closed.
func (recorder *AsyncRecorder) Record(
	venueID, symbol, timeframe string,
	candles []contracts.Candle,
) bool {
	if recorder == nil || recorder.closing.Load() {
		return false
	}
	closed, err := ValidatedClosedCandles(timeframe, candles, recorder.now())
	if err != nil || len(closed) == 0 {
		if err != nil {
			recorder.metrics.RecordOHLCVError()
			recorder.logger.Warn("ohlcv_archive_rejected", "error", err.Error(), "timeframe", timeframe)
		}
		return false
	}
	batch := recordBatch{venue: venueID, symbol: symbol, timeframe: timeframe, candles: closed}
	select {
	case recorder.queue <- batch:
		recorder.metrics.RecordOHLCVQueued(uint64(len(closed)))
		return true
	default:
		recorder.metrics.RecordOHLCVDropped(uint64(len(closed)))
		recorder.logger.Warn("ohlcv_archive_queue_full", "venue", venueID, "symbol", symbol,
			"timeframe", timeframe, "candles", len(closed))
		return false
	}
}

func (recorder *AsyncRecorder) run(ctx context.Context) {
	defer close(recorder.done)
	recorder.prune()
	ticker := time.NewTicker(recorder.cleanupEvery)
	defer ticker.Stop()
	for {
		select {
		case batch := <-recorder.queue:
			recorder.save(batch)
		case <-ticker.C:
			recorder.prune()
		case <-ctx.Done():
			for {
				select {
				case batch := <-recorder.queue:
					recorder.save(batch)
				default:
					return
				}
			}
		}
	}
}

func (recorder *AsyncRecorder) save(batch recordBatch) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), recorder.writeTimeout)
		err = recorder.store.Save(ctx, batch.venue, batch.symbol, batch.timeframe, batch.candles)
		cancel()
		if err == nil {
			recorder.metrics.RecordOHLCVSaved(uint64(len(batch.candles)))
			return
		}
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
		}
	}
	recorder.metrics.RecordOHLCVError()
	recorder.metrics.RecordOHLCVDropped(uint64(len(batch.candles)))
	recorder.logger.Error("ohlcv_archive_write_failed", "error", err.Error(), "venue", batch.venue,
		"symbol", batch.symbol, "timeframe", batch.timeframe, "candles", len(batch.candles))
}

func (recorder *AsyncRecorder) prune() {
	ctx, cancel := context.WithTimeout(context.Background(), recorder.writeTimeout)
	removed, err := recorder.store.Prune(ctx, recorder.now().Add(-recorder.retention))
	cancel()
	if err != nil {
		recorder.metrics.RecordOHLCVError()
		recorder.logger.Error("ohlcv_archive_prune_failed", "error", err.Error())
		return
	}
	if removed > 0 {
		recorder.metrics.RecordOHLCVPruned(uint64(removed))
		recorder.logger.Info("ohlcv_archive_pruned", "candles", removed,
			"retention_days", int(recorder.retention/(24*time.Hour)))
	}
}

func (recorder *AsyncRecorder) Close(ctx context.Context) error {
	if recorder == nil {
		return nil
	}
	recorder.closeOnce.Do(func() {
		recorder.closing.Store(true)
		recorder.cancel()
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-recorder.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
