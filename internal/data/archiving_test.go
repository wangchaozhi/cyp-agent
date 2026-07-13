package data

import (
	"context"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type sourceStub struct{ snapshot contracts.MarketSnapshot }

func (source sourceStub) Snapshot(context.Context, string) (contracts.MarketSnapshot, error) {
	return source.snapshot, nil
}

type recorderStub struct {
	venue, symbol, timeframe string
	candles                  []contracts.Candle
}

func (recorder *recorderStub) Record(venue, symbol, timeframe string, candles []contracts.Candle) bool {
	recorder.venue, recorder.symbol, recorder.timeframe = venue, symbol, timeframe
	recorder.candles = append([]contracts.Candle(nil), candles...)
	return true
}

func TestArchivingSourceCopiesSuccessfulSnapshotToRecorder(t *testing.T) {
	candle := contracts.Candle{TS: time.Now().Add(-2 * time.Hour)}
	recorder := &recorderStub{}
	source, err := NewArchivingSource(sourceStub{snapshot: contracts.MarketSnapshot{
		Venue: "okx", Symbol: "BTC/USDT:USDT", OHLCV: []contracts.Candle{candle},
	}}, recorder, "1h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Snapshot(context.Background(), "BTC/USDT:USDT"); err != nil {
		t.Fatal(err)
	}
	if recorder.venue != "okx" || recorder.symbol != "BTC/USDT:USDT" ||
		recorder.timeframe != "1h" || len(recorder.candles) != 1 {
		t.Fatalf("recorded=%+v", recorder)
	}
}
