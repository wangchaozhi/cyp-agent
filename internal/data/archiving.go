package data

import (
	"context"
	"errors"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type CandleRecorder interface {
	Record(string, string, string, []contracts.Candle) bool
}

type ArchivingSource struct {
	source    Source
	recorder  CandleRecorder
	timeframe string
}

func NewArchivingSource(source Source, recorder CandleRecorder, timeframe string) (*ArchivingSource, error) {
	if source == nil || recorder == nil {
		return nil, errors.New("archiving source requires source and recorder")
	}
	timeframe = strings.ToLower(strings.TrimSpace(timeframe))
	if timeframe == "" {
		return nil, errors.New("archiving source timeframe is required")
	}
	return &ArchivingSource{source: source, recorder: recorder, timeframe: timeframe}, nil
}

func (source *ArchivingSource) Snapshot(ctx context.Context, symbol string) (contracts.MarketSnapshot, error) {
	snapshot, err := source.source.Snapshot(ctx, symbol)
	if err != nil {
		return contracts.MarketSnapshot{}, err
	}
	source.recorder.Record(snapshot.Venue, snapshot.Symbol, source.timeframe, snapshot.OHLCV)
	return snapshot, nil
}

var _ Source = (*ArchivingSource)(nil)
