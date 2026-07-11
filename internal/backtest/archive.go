package backtest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

var timeframeDurations = map[string]time.Duration{
	"1m": time.Minute, "5m": 5 * time.Minute, "15m": 15 * time.Minute,
	"30m": 30 * time.Minute, "1h": time.Hour, "4h": 4 * time.Hour,
	"1d": 24 * time.Hour,
}

func TimeframeDuration(timeframe string) (time.Duration, error) {
	duration, ok := timeframeDurations[timeframe]
	if !ok {
		return 0, fmt.Errorf("unsupported timeframe %q", timeframe)
	}
	return duration, nil
}

type HistoricalVenue interface {
	ID() string
	FetchOHLCV(context.Context, string, string, int) ([]contracts.Candle, error)
}

type OHLCVArchive struct{ pool *pgxpool.Pool }

func NewOHLCVArchive(ctx context.Context, dsn string) (*OHLCVArchive, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("PostgreSQL DSN is required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open OHLCV archive: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping OHLCV archive: %w", err)
	}
	archive := &OHLCVArchive{pool: pool}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS ohlcv (
        venue TEXT NOT NULL,
        symbol TEXT NOT NULL,
        timeframe TEXT NOT NULL,
        ts TIMESTAMPTZ NOT NULL,
        open NUMERIC NOT NULL,
        high NUMERIC NOT NULL,
        low NUMERIC NOT NULL,
        close NUMERIC NOT NULL,
        volume NUMERIC NOT NULL,
        PRIMARY KEY (venue, symbol, timeframe, ts)
    )`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("initialize OHLCV archive: %w", err)
	}
	return archive, nil
}

func (archive *OHLCVArchive) Load(
	ctx context.Context,
	venueID, symbol, timeframe string,
	bars int,
) ([]contracts.Candle, error) {
	if archive == nil || archive.pool == nil {
		return nil, errors.New("OHLCV archive is closed")
	}
	if bars <= 0 {
		return []contracts.Candle{}, nil
	}
	if _, err := TimeframeDuration(timeframe); err != nil {
		return nil, err
	}
	rows, err := archive.pool.Query(ctx, `
        SELECT ts, open::text, high::text, low::text, close::text, volume::text
        FROM ohlcv WHERE venue=$1 AND symbol=$2 AND timeframe=$3
        ORDER BY ts DESC LIMIT $4`, venueID, symbol, timeframe, bars)
	if err != nil {
		return nil, fmt.Errorf("load OHLCV archive: %w", err)
	}
	defer rows.Close()
	result := make([]contracts.Candle, 0, bars)
	for rows.Next() {
		var timestamp time.Time
		var values [5]string
		if err := rows.Scan(&timestamp, &values[0], &values[1], &values[2], &values[3], &values[4]); err != nil {
			return nil, fmt.Errorf("scan OHLCV archive: %w", err)
		}
		parsed := [5]contracts.Decimal{}
		for index, value := range values {
			decimal, err := contracts.ParseDecimal(value)
			if err != nil {
				return nil, fmt.Errorf("decode OHLCV decimal: %w", err)
			}
			parsed[index] = decimal
		}
		result = append(result, contracts.Candle{
			TS: timestamp.UTC(), Open: parsed[0], High: parsed[1], Low: parsed[2],
			Close: parsed[3], Volume: parsed[4],
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate OHLCV archive: %w", err)
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result, nil
}

func (archive *OHLCVArchive) Ensure(
	ctx context.Context,
	market HistoricalVenue,
	symbol, timeframe string,
	bars int,
) ([]contracts.Candle, error) {
	if market == nil {
		return nil, errors.New("historical venue is required")
	}
	cached, err := archive.Load(ctx, market.ID(), symbol, timeframe, bars)
	if err != nil {
		return nil, err
	}
	if len(cached) >= bars {
		return cached[len(cached)-bars:], nil
	}
	fetched, err := market.FetchOHLCV(ctx, symbol, timeframe, bars)
	if err != nil {
		return nil, fmt.Errorf("fetch OHLCV history: %w", err)
	}
	merged := make(map[int64]contracts.Candle, len(cached)+len(fetched))
	for _, candle := range append(cached, fetched...) {
		merged[candle.TS.UTC().UnixNano()] = candle
	}
	result := make([]contracts.Candle, 0, len(merged))
	for _, candle := range merged {
		result = append(result, candle)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TS.Before(result[j].TS) })
	if err := archive.Save(ctx, market.ID(), symbol, timeframe, result); err != nil {
		return nil, err
	}
	if len(result) > bars {
		result = result[len(result)-bars:]
	}
	return result, nil
}

func (archive *OHLCVArchive) Save(
	ctx context.Context,
	venueID, symbol, timeframe string,
	candles []contracts.Candle,
) error {
	if len(candles) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, candle := range candles {
		batch.Queue(`INSERT INTO ohlcv
            (venue, symbol, timeframe, ts, open, high, low, close, volume)
            VALUES ($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,$8::numeric,$9::numeric)
            ON CONFLICT (venue, symbol, timeframe, ts) DO UPDATE SET
            open=EXCLUDED.open, high=EXCLUDED.high, low=EXCLUDED.low,
            close=EXCLUDED.close, volume=EXCLUDED.volume`,
			venueID, symbol, timeframe, candle.TS.UTC(), candle.Open.String(), candle.High.String(),
			candle.Low.String(), candle.Close.String(), candle.Volume.String())
	}
	results := archive.pool.SendBatch(ctx, batch)
	for range candles {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("save OHLCV archive: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("close OHLCV batch: %w", err)
	}
	return nil
}

func (archive *OHLCVArchive) Close() {
	if archive != nil && archive.pool != nil {
		archive.pool.Close()
	}
}
