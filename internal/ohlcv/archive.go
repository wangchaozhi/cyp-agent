// Package ohlcv provides durable, validated market-candle archival without
// putting PostgreSQL on the latency-sensitive trading path.
package ohlcv

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
	duration, ok := timeframeDurations[strings.ToLower(strings.TrimSpace(timeframe))]
	if !ok {
		return 0, fmt.Errorf("unsupported timeframe %q", timeframe)
	}
	return duration, nil
}

// ValidatedClosedCandles rejects forming, malformed and duplicate bars. A
// timestamp is interpreted as the opening time used by Binance and OKX.
func ValidatedClosedCandles(
	timeframe string,
	candles []contracts.Candle,
	now time.Time,
) ([]contracts.Candle, error) {
	duration, err := TimeframeDuration(timeframe)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	unique := make(map[int64]contracts.Candle, len(candles))
	for _, candle := range candles {
		candle.TS = candle.TS.UTC()
		if !validCandle(candle) || candle.TS.Add(duration).After(now) {
			continue
		}
		unique[candle.TS.UnixNano()] = candle
	}
	result := make([]contracts.Candle, 0, len(unique))
	for _, candle := range unique {
		result = append(result, candle)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TS.Before(result[j].TS) })
	return result, nil
}

func validCandle(candle contracts.Candle) bool {
	if candle.TS.IsZero() || !candle.Open.IsPositive() || !candle.High.IsPositive() ||
		!candle.Low.IsPositive() || !candle.Close.IsPositive() || candle.Volume.IsNegative() {
		return false
	}
	if candle.Low.Cmp(candle.High) > 0 {
		return false
	}
	return candle.Open.Cmp(candle.Low) >= 0 && candle.Open.Cmp(candle.High) <= 0 &&
		candle.Close.Cmp(candle.Low) >= 0 && candle.Close.Cmp(candle.High) <= 0
}

type HistoricalVenue interface {
	ID() string
	FetchOHLCV(context.Context, string, string, int) ([]contracts.Candle, error)
}

type RangeHistoricalVenue interface {
	HistoricalVenue
	FetchOHLCVRange(context.Context, string, string, time.Time, time.Time, int) ([]contracts.Candle, error)
}

type Archive interface {
	Load(context.Context, string, string, string, int) ([]contracts.Candle, error)
	Ensure(context.Context, HistoricalVenue, string, string, int) ([]contracts.Candle, error)
	Repair(context.Context, HistoricalVenue, string, string, time.Duration, time.Time) (int, error)
	Save(context.Context, string, string, string, []contracts.Candle) error
	Prune(context.Context, time.Time) (int64, error)
	Close()
}

type timeRange struct{ start, end time.Time }

// Repair finds missing bars in the retained window and fills each range using
// exchange pagination. It is safe to rerun because Save uses an upsert key.
func (archive *PostgresArchive) Repair(
	ctx context.Context,
	market HistoricalVenue,
	symbol, timeframe string,
	retention time.Duration,
	now time.Time,
) (int, error) {
	if market == nil {
		return 0, errors.New("historical venue is required")
	}
	duration, err := TimeframeDuration(timeframe)
	if err != nil {
		return 0, err
	}
	if retention <= 0 {
		return 0, errors.New("OHLCV repair retention must be positive")
	}
	end := now.UTC().Truncate(duration)
	start := end.Add(-retention)
	maxBars := int(retention/duration) + 2
	cached, err := archive.Load(ctx, market.ID(), symbol, timeframe, maxBars)
	if err != nil {
		return 0, err
	}
	ranges := missingTimeRanges(cached, start, end, duration)
	if len(ranges) == 0 {
		return 0, nil
	}
	ranged, supportsRange := market.(RangeHistoricalVenue)
	if !supportsRange {
		latest, fetchErr := market.FetchOHLCV(ctx, symbol, timeframe, minInt(maxBars, 200))
		if fetchErr != nil {
			return 0, fmt.Errorf("repair OHLCV history: %w", fetchErr)
		}
		closed, validationErr := ValidatedClosedCandles(timeframe, latest, now)
		if validationErr != nil {
			return 0, validationErr
		}
		if err := archive.Save(ctx, market.ID(), symbol, timeframe, closed); err != nil {
			return 0, err
		}
		return len(closed), nil
	}
	repaired := 0
	for _, missing := range ranges {
		candles, fetchErr := ranged.FetchOHLCVRange(ctx, symbol, timeframe, missing.start, missing.end, 300)
		if fetchErr != nil {
			return repaired, fmt.Errorf("repair OHLCV range %s..%s: %w",
				missing.start.Format(time.RFC3339), missing.end.Format(time.RFC3339), fetchErr)
		}
		closed, validationErr := ValidatedClosedCandles(timeframe, candles, now)
		if validationErr != nil {
			return repaired, validationErr
		}
		if err := archive.Save(ctx, market.ID(), symbol, timeframe, closed); err != nil {
			return repaired, err
		}
		repaired += len(closed)
	}
	return repaired, nil
}

func missingTimeRanges(candles []contracts.Candle, start, end time.Time, duration time.Duration) []timeRange {
	if !start.Before(end) {
		return nil
	}
	if len(candles) == 0 {
		return []timeRange{{start: start, end: end}}
	}
	sorted := append([]contracts.Candle(nil), candles...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TS.Before(sorted[j].TS) })
	result := make([]timeRange, 0)
	cursor := start
	for _, candle := range sorted {
		timestamp := candle.TS.UTC()
		if timestamp.Before(start) || !timestamp.Before(end) {
			continue
		}
		if timestamp.After(cursor) {
			result = append(result, timeRange{start: cursor, end: timestamp})
		}
		if next := timestamp.Add(duration); next.After(cursor) {
			cursor = next
		}
	}
	if cursor.Before(end) {
		result = append(result, timeRange{start: cursor, end: end})
	}
	return result
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

type PostgresArchive struct{ pool *pgxpool.Pool }

func NewPostgresArchive(ctx context.Context, dsn string) (*PostgresArchive, error) {
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
	archive := &PostgresArchive{pool: pool}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ohlcv (
            venue TEXT NOT NULL,
            symbol TEXT NOT NULL,
            timeframe TEXT NOT NULL,
            ts TIMESTAMPTZ NOT NULL,
            open NUMERIC NOT NULL,
            high NUMERIC NOT NULL,
            low NUMERIC NOT NULL,
            close NUMERIC NOT NULL,
            volume NUMERIC NOT NULL,
            ingested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
            quality_status TEXT NOT NULL DEFAULT 'validated',
            PRIMARY KEY (venue, symbol, timeframe, ts)
        )`,
		`ALTER TABLE ohlcv ADD COLUMN IF NOT EXISTS ingested_at TIMESTAMPTZ NOT NULL DEFAULT now()`,
		`ALTER TABLE ohlcv ADD COLUMN IF NOT EXISTS quality_status TEXT NOT NULL DEFAULT 'validated'`,
		`CREATE INDEX IF NOT EXISTS ohlcv_symbol_timeframe_ts_idx
            ON ohlcv (symbol, timeframe, ts DESC)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			pool.Close()
			return nil, fmt.Errorf("initialize OHLCV archive: %w", err)
		}
	}
	return archive, nil
}

func (archive *PostgresArchive) Load(
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
        FROM ohlcv
        WHERE venue=$1 AND symbol=$2 AND timeframe=$3 AND quality_status='validated'
        ORDER BY ts DESC LIMIT $4`, strings.TrimSpace(venueID), strings.TrimSpace(symbol),
		strings.ToLower(strings.TrimSpace(timeframe)), bars)
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

// Ensure always refreshes from the venue, merges with PostgreSQL and returns
// only validated closed bars. When the venue is temporarily unavailable, a
// sufficiently deep archive remains usable for research.
func (archive *PostgresArchive) Ensure(
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
	now := time.Now()
	var fetched []contracts.Candle
	var fetchErr error
	if ranged, ok := market.(RangeHistoricalVenue); ok {
		duration, durationErr := TimeframeDuration(timeframe)
		if durationErr != nil {
			return nil, durationErr
		}
		end := now.UTC().Truncate(duration)
		start := end.Add(-time.Duration(bars) * duration)
		fetched, fetchErr = ranged.FetchOHLCVRange(ctx, symbol, timeframe, start, end, 300)
	} else {
		fetched, fetchErr = market.FetchOHLCV(ctx, symbol, timeframe, minInt(bars, 200))
	}
	if fetchErr != nil {
		if len(cached) >= bars {
			return cached[len(cached)-bars:], nil
		}
		return nil, fmt.Errorf("fetch OHLCV history: %w", fetchErr)
	}
	closed, err := ValidatedClosedCandles(timeframe, fetched, now)
	if err != nil {
		return nil, err
	}
	merged := make(map[int64]contracts.Candle, len(cached)+len(closed))
	for _, candle := range append(cached, closed...) {
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

func (archive *PostgresArchive) Save(
	ctx context.Context,
	venueID, symbol, timeframe string,
	candles []contracts.Candle,
) error {
	if archive == nil || archive.pool == nil {
		return errors.New("OHLCV archive is closed")
	}
	venueID, symbol = strings.TrimSpace(venueID), strings.TrimSpace(symbol)
	if venueID == "" || symbol == "" {
		return errors.New("OHLCV venue and symbol are required")
	}
	timeframe = strings.ToLower(strings.TrimSpace(timeframe))
	closed, err := ValidatedClosedCandles(timeframe, candles, time.Now())
	if err != nil {
		return err
	}
	if len(closed) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, candle := range closed {
		batch.Queue(`INSERT INTO ohlcv
            (venue, symbol, timeframe, ts, open, high, low, close, volume, ingested_at, quality_status)
            VALUES ($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,$8::numeric,$9::numeric,now(),'validated')
            ON CONFLICT (venue, symbol, timeframe, ts) DO UPDATE SET
            open=EXCLUDED.open, high=EXCLUDED.high, low=EXCLUDED.low,
            close=EXCLUDED.close, volume=EXCLUDED.volume,
            ingested_at=now(), quality_status='validated'`,
			venueID, symbol, timeframe, candle.TS.UTC(), candle.Open.String(), candle.High.String(),
			candle.Low.String(), candle.Close.String(), candle.Volume.String())
	}
	results := archive.pool.SendBatch(ctx, batch)
	for range closed {
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

func (archive *PostgresArchive) Prune(ctx context.Context, before time.Time) (int64, error) {
	if archive == nil || archive.pool == nil {
		return 0, errors.New("OHLCV archive is closed")
	}
	result, err := archive.pool.Exec(ctx, `DELETE FROM ohlcv WHERE ts < $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune OHLCV archive: %w", err)
	}
	return result.RowsAffected(), nil
}

func (archive *PostgresArchive) Close() {
	if archive != nil && archive.pool != nil {
		archive.pool.Close()
	}
}

var _ Archive = (*PostgresArchive)(nil)
