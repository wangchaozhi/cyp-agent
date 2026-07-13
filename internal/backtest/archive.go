package backtest

import (
	"context"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/ohlcv"
)

// Backward-compatible aliases keep the research API stable while the archive
// is shared by live collection, chart history and backtests.
type HistoricalVenue = ohlcv.HistoricalVenue
type OHLCVArchive = ohlcv.PostgresArchive

func NewOHLCVArchive(ctx context.Context, dsn string) (*OHLCVArchive, error) {
	return ohlcv.NewPostgresArchive(ctx, dsn)
}

func TimeframeDuration(timeframe string) (time.Duration, error) {
	return ohlcv.TimeframeDuration(timeframe)
}
