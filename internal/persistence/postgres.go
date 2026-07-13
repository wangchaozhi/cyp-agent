package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRepository preserves the legacy lessons/checkpoints tables while
// giving the Go runtime a concurrency-safe pooled implementation.
type PostgresRepository struct {
	pool       *pgxpool.Pool
	maxLessons int
}

func NewPostgresRepository(ctx context.Context, dsn string, maxLessons int) (*PostgresRepository, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("PostgreSQL DSN is required")
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL config: %w", err)
	}
	poolConfig.MaxConns = 4
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.MaxConnLifetime = time.Hour
	poolConfig.HealthCheckPeriod = 30 * time.Second
	poolConfig.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	poolConfig.ConnConfig.RuntimeParams["lock_timeout"] = "3000"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	repository := &PostgresRepository{pool: pool, maxLessons: normalizeMaxLessons(maxLessons)}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	if err := repository.ensureSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return repository, nil
}

func (repository *PostgresRepository) ensureSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS lessons (
            id BIGSERIAL PRIMARY KEY,
            symbol TEXT NOT NULL DEFAULT '',
            text TEXT NOT NULL
        )`,
		`ALTER TABLE lessons ADD COLUMN IF NOT EXISTS ts TIMESTAMPTZ NOT NULL DEFAULT now()`,
		`CREATE TABLE IF NOT EXISTS checkpoints (
            run_id TEXT NOT NULL,
            step TEXT NOT NULL,
            data TEXT NOT NULL,
            PRIMARY KEY (run_id, step)
        )`,
		`ALTER TABLE checkpoints ADD COLUMN IF NOT EXISTS ts TIMESTAMPTZ NOT NULL DEFAULT now()`,
	}
	for _, statement := range statements {
		if _, err := repository.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("initialize PostgreSQL repository schema: %w", err)
		}
	}
	return nil
}

func (repository *PostgresRepository) SaveCheckpoint(
	ctx context.Context,
	runID, step string,
	value any,
) error {
	return repository.SaveCheckpoints(ctx, runID, map[string]any{step: value})
}

func (repository *PostgresRepository) SaveCheckpoints(
	ctx context.Context,
	runID string,
	values map[string]any,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ErrInvalidRunID
	}
	encoded, err := encodeCheckpoints(values)
	if err != nil {
		return err
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin PostgreSQL checkpoint transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	for step, raw := range encoded {
		if _, err := transaction.Exec(ctx, `
            INSERT INTO checkpoints (run_id, step, data, ts)
            VALUES ($1, $2, $3, now())
            ON CONFLICT (run_id, step) DO UPDATE
            SET data = EXCLUDED.data, ts = EXCLUDED.ts`, runID, step, string(raw)); err != nil {
			return fmt.Errorf("save PostgreSQL checkpoint: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit PostgreSQL checkpoints: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) LoadCheckpoints(
	ctx context.Context,
	runID string,
) (map[string]json.RawMessage, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, ErrInvalidRunID
	}
	rows, err := repository.pool.Query(ctx,
		`SELECT step, data FROM checkpoints WHERE run_id=$1 ORDER BY step`, runID)
	if err != nil {
		return nil, fmt.Errorf("load PostgreSQL checkpoints: %w", err)
	}
	defer rows.Close()
	result := make(map[string]json.RawMessage)
	for rows.Next() {
		var step, data string
		if err := rows.Scan(&step, &data); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL checkpoint: %w", err)
		}
		raw := json.RawMessage(data)
		if !json.Valid(raw) {
			return nil, fmt.Errorf("checkpoint %s/%s contains invalid JSON", runID, step)
		}
		result[step] = cloneRaw(raw)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PostgreSQL checkpoints: %w", err)
	}
	return result, nil
}

func (repository *PostgresRepository) PruneCheckpoints(ctx context.Context, keepRecentRuns int) (int, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	if keepRecentRuns <= 0 {
		return 0, ErrInvalidKeep
	}
	var removed int
	err := repository.pool.QueryRow(ctx, `
        WITH expired AS (
            SELECT run_id
            FROM checkpoints
            WHERE LEFT(run_id, 2) <> '__'
            GROUP BY run_id
            ORDER BY MAX(ts) DESC, run_id DESC
            OFFSET $1
		), deleted AS (
			DELETE FROM checkpoints AS target
			USING expired
			WHERE target.run_id = expired.run_id
			RETURNING target.run_id
		)
		SELECT COUNT(DISTINCT run_id) FROM deleted`, keepRecentRuns).Scan(&removed)
	if err != nil {
		return 0, fmt.Errorf("prune PostgreSQL checkpoints: %w", err)
	}
	return removed, nil
}

func (repository *PostgresRepository) AppendLessons(
	ctx context.Context,
	symbol string,
	lessons []string,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin PostgreSQL lessons transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	for _, lesson := range lessons {
		lesson = strings.TrimSpace(lesson)
		if lesson == "" {
			continue
		}
		if _, err := transaction.Exec(ctx,
			`INSERT INTO lessons (symbol, text, ts) VALUES ($1, $2, now())`,
			strings.TrimSpace(symbol), lesson); err != nil {
			return fmt.Errorf("append PostgreSQL lesson: %w", err)
		}
	}
	if _, err := transaction.Exec(ctx, `
        DELETE FROM lessons
        WHERE id IN (SELECT id FROM lessons ORDER BY id DESC OFFSET $1)`, repository.maxLessons); err != nil {
		return fmt.Errorf("trim PostgreSQL lessons: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit PostgreSQL lessons: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) GetLessons(
	ctx context.Context,
	limit int,
	symbol string,
) ([]string, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []string{}, nil
	}
	rows, err := repository.pool.Query(ctx, `
        SELECT id, symbol, text, ts
        FROM lessons ORDER BY id DESC LIMIT $1`, maxInt(limit*20, limit))
	if err != nil {
		return nil, fmt.Errorf("load PostgreSQL lessons: %w", err)
	}
	defer rows.Close()
	records := make([]lessonRecord, 0)
	for rows.Next() {
		var record lessonRecord
		if err := rows.Scan(&record.ID, &record.Symbol, &record.Text, &record.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL lesson: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PostgreSQL lessons: %w", err)
	}
	// Query is newest-first; the shared relevance implementation expects
	// chronological records and adds a tiny recency tie-breaker.
	for left, right := 0, len(records)-1; left < right; left, right = left+1, right-1 {
		records[left], records[right] = records[right], records[left]
	}
	state := newRepositoryState()
	state.Lessons = records
	return getLessons(state, limit, symbol), nil
}

func (repository *PostgresRepository) Close() error {
	if repository != nil && repository.pool != nil {
		repository.pool.Close()
	}
	return nil
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

var _ Repository = (*PostgresRepository)(nil)
