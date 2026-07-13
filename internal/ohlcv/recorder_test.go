package ohlcv

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/observability"
)

type recorderStoreStub struct {
	mu      sync.Mutex
	saved   []contracts.Candle
	saveHit chan struct{}
}

func (store *recorderStoreStub) Save(
	_ context.Context, _, _, _ string, candles []contracts.Candle,
) error {
	store.mu.Lock()
	store.saved = append(store.saved, candles...)
	store.mu.Unlock()
	select {
	case store.saveHit <- struct{}{}:
	default:
	}
	return nil
}

func (*recorderStoreStub) Prune(context.Context, time.Time) (int64, error) { return 0, nil }

func TestAsyncRecorderDoesNotQueueFormingCandles(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 30, 0, 0, time.UTC)
	store := &recorderStoreStub{saveHit: make(chan struct{}, 1)}
	metrics := &observability.RuntimeMetrics{}
	recorder, err := NewAsyncRecorder(RecorderConfig{
		Store: store, Retention: 730 * 24 * time.Hour, QueueSize: 2,
		WriteTimeout: time.Second, CleanupInterval: time.Hour,
		Metrics: metrics, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !recorder.Record("okx", "BTC/USDT:USDT", "1h", []contracts.Candle{
		testCandle(now.Add(-2*time.Hour), "100", "110", "90", "105", "12"),
		testCandle(now.Truncate(time.Hour), "105", "112", "104", "110", "5"),
	}) {
		t.Fatal("closed candle should be queued")
	}
	select {
	case <-store.saveHit:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for archive write")
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	saved := append([]contracts.Candle(nil), store.saved...)
	store.mu.Unlock()
	if len(saved) != 1 {
		t.Fatalf("saved candles=%d", len(saved))
	}
	snapshot := metrics.Snapshot()
	if snapshot.OHLCVQueued != 1 || snapshot.OHLCVSaved != 1 || snapshot.OHLCVDropped != 0 {
		t.Fatalf("metrics=%+v", snapshot)
	}
}
