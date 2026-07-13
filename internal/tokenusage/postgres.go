package tokenusage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wangchaozhi/cyp-agent/internal/llm"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("PostgreSQL DSN is required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open token usage store: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping token usage store: %w", err)
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS llm_usage_events (
            id TEXT NOT NULL,
            ts TIMESTAMPTZ NOT NULL,
            run_id TEXT NOT NULL DEFAULT '', symbol TEXT NOT NULL DEFAULT '',
            agent TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '',
            provider TEXT NOT NULL, model TEXT NOT NULL, operation TEXT NOT NULL,
            status TEXT NOT NULL, input_tokens BIGINT NOT NULL DEFAULT 0,
            output_tokens BIGINT NOT NULL DEFAULT 0, cost_usd NUMERIC NOT NULL DEFAULT 0,
            duration_ms BIGINT NOT NULL DEFAULT 0, token_estimated BOOLEAN NOT NULL DEFAULT FALSE,
            cost_estimated BOOLEAN NOT NULL DEFAULT FALSE, error_kind TEXT NOT NULL DEFAULT '',
            created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY (id, ts)
        )`,
		`CREATE INDEX IF NOT EXISTS llm_usage_events_ts_idx ON llm_usage_events (ts DESC)`,
		`CREATE INDEX IF NOT EXISTS llm_usage_events_run_idx ON llm_usage_events (run_id, ts DESC)`,
		`CREATE INDEX IF NOT EXISTS llm_usage_events_dimensions_idx ON llm_usage_events (provider, model, agent, symbol, ts DESC)`,
		`CREATE TABLE IF NOT EXISTS llm_usage_daily (
            day DATE NOT NULL, timezone TEXT NOT NULL, provider TEXT NOT NULL,
            model TEXT NOT NULL, agent TEXT NOT NULL, symbol TEXT NOT NULL,
            source TEXT NOT NULL, status TEXT NOT NULL,
            calls BIGINT NOT NULL DEFAULT 0, successes BIGINT NOT NULL DEFAULT 0,
            input_tokens BIGINT NOT NULL DEFAULT 0, output_tokens BIGINT NOT NULL DEFAULT 0,
            cost_usd NUMERIC NOT NULL DEFAULT 0, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
            PRIMARY KEY (day, timezone, provider, model, agent, symbol, source, status)
        )`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			pool.Close()
			return nil, fmt.Errorf("initialize token usage store: %w", err)
		}
	}
	return &PostgresStore{pool: pool}, nil
}

func (store *PostgresStore) Load(ctx context.Context, since time.Time) ([]llm.UsageEvent, error) {
	if store == nil || store.pool == nil {
		return nil, errors.New("token usage store is closed")
	}
	rows, err := store.pool.Query(ctx, `SELECT id,ts,run_id,symbol,agent,source,provider,model,
        operation,status,input_tokens,output_tokens,cost_usd::float8,duration_ms,
        token_estimated,cost_estimated,error_kind FROM llm_usage_events WHERE ts >= $1 ORDER BY ts`, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("load token usage: %w", err)
	}
	defer rows.Close()
	result := make([]llm.UsageEvent, 0)
	for rows.Next() {
		var event llm.UsageEvent
		if err := rows.Scan(&event.ID, &event.TS, &event.RunID, &event.Symbol, &event.Agent, &event.Source,
			&event.Provider, &event.Model, &event.Operation, &event.Status, &event.InputTokens,
			&event.OutputTokens, &event.CostUSD, &event.DurationMS, &event.TokenEstimated,
			&event.CostEstimated, &event.ErrorKind); err != nil {
			return nil, fmt.Errorf("scan token usage: %w", err)
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate token usage: %w", err)
	}
	return result, nil
}

func (store *PostgresStore) Save(ctx context.Context, event llm.UsageEvent, day, timezone string) error {
	if store == nil || store.pool == nil {
		return errors.New("token usage store is closed")
	}
	_, err := store.pool.Exec(ctx, `WITH inserted AS (
        INSERT INTO llm_usage_events (id,ts,run_id,symbol,agent,source,provider,model,operation,status,
            input_tokens,output_tokens,cost_usd,duration_ms,token_estimated,cost_estimated,error_kind)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
        ON CONFLICT (id,ts) DO NOTHING RETURNING 1
    )
    INSERT INTO llm_usage_daily (day,timezone,provider,model,agent,symbol,source,status,calls,successes,
        input_tokens,output_tokens,cost_usd)
    SELECT $18::date,$19,$7,$8,$5,$4,$6,$10,1,CASE WHEN $10='success' THEN 1 ELSE 0 END,$11,$12,$13
    WHERE EXISTS (SELECT 1 FROM inserted)
    ON CONFLICT (day,timezone,provider,model,agent,symbol,source,status) DO UPDATE SET
        calls=llm_usage_daily.calls+1,
        successes=llm_usage_daily.successes+EXCLUDED.successes,
        input_tokens=llm_usage_daily.input_tokens+EXCLUDED.input_tokens,
        output_tokens=llm_usage_daily.output_tokens+EXCLUDED.output_tokens,
        cost_usd=llm_usage_daily.cost_usd+EXCLUDED.cost_usd,updated_at=now()`,
		event.ID, event.TS.UTC(), event.RunID, event.Symbol, event.Agent, event.Source, event.Provider,
		event.Model, event.Operation, event.Status, event.InputTokens, event.OutputTokens, event.CostUSD,
		event.DurationMS, event.TokenEstimated, event.CostEstimated, event.ErrorKind, day, timezone)
	if err != nil {
		return fmt.Errorf("save token usage: %w", err)
	}
	return nil
}

func (store *PostgresStore) Prune(ctx context.Context, before time.Time) (int64, error) {
	if store == nil || store.pool == nil {
		return 0, errors.New("token usage store is closed")
	}
	result, err := store.pool.Exec(ctx, `DELETE FROM llm_usage_events WHERE ts < $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune token usage: %w", err)
	}
	return result.RowsAffected(), nil
}

func (store *PostgresStore) Close() {
	if store != nil && store.pool != nil {
		store.pool.Close()
	}
}

var _ Store = (*PostgresStore)(nil)
var _ llm.UsageObserver = (*Tracker)(nil)
