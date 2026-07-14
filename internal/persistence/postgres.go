package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/orders"
)

// PostgresRepository preserves the legacy lessons/checkpoints tables while
// giving the Go runtime a concurrency-safe pooled implementation.
type PostgresRepository struct {
	pool       *pgxpool.Pool
	maxLessons int
	leaseMu    sync.Mutex
	leaseConn  *pgxpool.Conn
	leaseKey   int64
	leasePID   int32
	leaseScope string
}

var ErrExecutionLeaseHeld = errors.New("execution account is already owned by another process")

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
		`CREATE TABLE IF NOT EXISTS orders (
            client_id TEXT PRIMARY KEY,
            run_id TEXT,
            venue TEXT NOT NULL,
            symbol TEXT NOT NULL,
            status TEXT NOT NULL,
            intent JSONB NOT NULL,
            result JSONB,
            created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
        )`,
		`CREATE TABLE IF NOT EXISTS order_events (
            id BIGSERIAL PRIMARY KEY,
            event_id TEXT,
            client_id TEXT NOT NULL REFERENCES orders(client_id),
            ts TIMESTAMPTZ NOT NULL DEFAULT now(),
            type TEXT NOT NULL,
            payload JSONB NOT NULL
        )`,
		`ALTER TABLE order_events ADD COLUMN IF NOT EXISTS event_id TEXT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS order_events_event_id_uidx
            ON order_events (event_id) WHERE event_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS order_events_client_ts_idx ON order_events (client_id, ts)`,
	}
	for _, statement := range statements {
		if _, err := repository.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("initialize PostgreSQL repository schema: %w", err)
		}
	}
	return nil
}

// AcquireExecutionLease takes a session-level PostgreSQL advisory lock and
// retains that exact pool connection for the process lifetime. The hashed
// scope avoids storing an API key or account identifier in pg_locks.
func (repository *PostgresRepository) AcquireExecutionLease(ctx context.Context, scope string) error {
	if repository == nil || repository.pool == nil {
		return errors.New("PostgreSQL repository is closed")
	}
	if ctx == nil {
		return errors.New("execution lease context is required")
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return errors.New("execution lease scope is required")
	}
	repository.leaseMu.Lock()
	defer repository.leaseMu.Unlock()
	if repository.leaseConn != nil {
		if repository.leaseScope != scope {
			return errors.New("repository already owns a different execution lease")
		}
		return repository.validateExecutionLeaseLocked(ctx)
	}

	digest := sha256.Sum256([]byte(scope))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	connection, err := repository.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire PostgreSQL execution lease connection: %w", err)
	}
	var acquired bool
	var backendPID int32
	if err := connection.QueryRow(ctx,
		`SELECT pg_try_advisory_lock($1), pg_backend_pid()`, key).Scan(&acquired, &backendPID); err != nil {
		connection.Release()
		return fmt.Errorf("acquire PostgreSQL execution lease: %w", err)
	}
	if !acquired {
		connection.Release()
		return ErrExecutionLeaseHeld
	}
	repository.leaseConn = connection
	repository.leaseKey = key
	repository.leasePID = backendPID
	repository.leaseScope = scope
	return nil
}

// ValidateExecutionLease proves the retained PostgreSQL session is still the
// one that acquired the advisory lock. Any disconnect fails closed before an
// exchange mutation can be attempted.
func (repository *PostgresRepository) ValidateExecutionLease(ctx context.Context) error {
	if repository == nil {
		return errors.New("PostgreSQL repository is closed")
	}
	repository.leaseMu.Lock()
	defer repository.leaseMu.Unlock()
	return repository.validateExecutionLeaseLocked(ctx)
}

func (repository *PostgresRepository) validateExecutionLeaseLocked(ctx context.Context) error {
	if repository.leaseConn == nil {
		return errors.New("execution lease is not acquired")
	}
	var backendPID int32
	if err := repository.leaseConn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID); err != nil {
		return fmt.Errorf("validate PostgreSQL execution lease: %w", err)
	}
	if backendPID != repository.leasePID {
		return errors.New("PostgreSQL execution lease session changed")
	}
	return nil
}

func (repository *PostgresRepository) AppendOrderEvent(ctx context.Context, event orders.Event) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	normalized, err := normalizeOrderEvent(event)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("encode PostgreSQL order event: %w", err)
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin PostgreSQL order event transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var existingPayload []byte
	queryErr := transaction.QueryRow(ctx,
		`SELECT payload FROM order_events WHERE event_id=$1`, normalized.EventID).Scan(&existingPayload)
	if queryErr == nil {
		var existing orders.Event
		if err := json.Unmarshal(existingPayload, &existing); err != nil {
			return fmt.Errorf("decode existing PostgreSQL order event: %w", err)
		}
		if !equivalentOrderEvent(existing, normalized) {
			return fmt.Errorf("order event id %s conflicts with persisted content", normalized.EventID)
		}
		return transaction.Commit(ctx)
	}
	if !errors.Is(queryErr, pgx.ErrNoRows) {
		return fmt.Errorf("inspect PostgreSQL order event: %w", queryErr)
	}

	if normalized.Status == contracts.OrderStatusNew {
		if normalized.Intent == nil {
			return ErrInvalidOrderEvent
		}
		intent, marshalErr := json.Marshal(normalized.Intent)
		if marshalErr != nil {
			return fmt.Errorf("encode PostgreSQL order intent: %w", marshalErr)
		}
		if _, err := transaction.Exec(ctx, `
            INSERT INTO orders (client_id, venue, symbol, status, intent, created_at, updated_at)
            VALUES ($1, $2, $3, $4, $5, $6, $6)
            ON CONFLICT (client_id) DO NOTHING`, normalized.ClientID, normalized.Intent.Venue,
			normalized.Intent.Symbol, normalized.Status, intent, normalized.TS); err != nil {
			return fmt.Errorf("create PostgreSQL order: %w", err)
		}
	}
	var orderExists bool
	if err := transaction.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM orders WHERE client_id=$1)`, normalized.ClientID).Scan(&orderExists); err != nil {
		return fmt.Errorf("inspect PostgreSQL order: %w", err)
	}
	if !orderExists {
		return fmt.Errorf("order %s does not exist", normalized.ClientID)
	}
	command, err := transaction.Exec(ctx, `
        INSERT INTO order_events (event_id, client_id, ts, type, payload)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT DO NOTHING`, normalized.EventID, normalized.ClientID,
		normalized.TS, normalized.Status, payload)
	if err != nil {
		return fmt.Errorf("append PostgreSQL order event: %w", err)
	}
	if command.RowsAffected() == 0 {
		return fmt.Errorf("order event %s raced with another writer; retry reconciliation", normalized.EventID)
	}
	var result []byte
	if normalized.Result != nil {
		result, err = json.Marshal(normalized.Result)
		if err != nil {
			return fmt.Errorf("encode PostgreSQL order result: %w", err)
		}
	}
	if _, err := transaction.Exec(ctx, `
        UPDATE orders SET status=$2, result=COALESCE($3::jsonb, result), updated_at=$4
        WHERE client_id=$1`, normalized.ClientID, normalized.Status, result, normalized.TS); err != nil {
		return fmt.Errorf("update PostgreSQL order: %w", err)
	}
	if orders.IsTerminal(normalized.Status) {
		if _, err := transaction.Exec(ctx, `
            WITH expired AS MATERIALIZED (
                SELECT target.client_id
                FROM orders AS target
                WHERE target.status = ANY($1)
                  AND EXISTS (
                      SELECT 1 FROM order_events AS journal
                      WHERE journal.client_id = target.client_id AND journal.event_id IS NOT NULL
                  )
                  AND NOT EXISTS (
                      SELECT 1 FROM order_events AS legacy
                      WHERE legacy.client_id = target.client_id AND legacy.event_id IS NULL
                  )
                ORDER BY target.updated_at DESC, target.client_id DESC
                OFFSET $2
            ), deleted_events AS (
                DELETE FROM order_events AS journal
                USING expired
                WHERE journal.client_id = expired.client_id
                RETURNING journal.client_id
            )
            DELETE FROM orders AS target
            USING expired
            WHERE target.client_id = expired.client_id`, []string{
			string(contracts.OrderStatusClosed), string(contracts.OrderStatusCanceled),
			string(contracts.OrderStatusRejected), string(contracts.OrderStatusFailed),
		}, defaultMaxTerminalOrders); err != nil {
			return fmt.Errorf("compact PostgreSQL order journal: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit PostgreSQL order event: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) LoadOrderEvents(ctx context.Context) ([]orders.Event, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	rows, err := repository.pool.Query(ctx, `
        SELECT payload FROM order_events
        WHERE event_id IS NOT NULL
        ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load PostgreSQL order events: %w", err)
	}
	defer rows.Close()
	result := make([]orders.Event, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL order event: %w", err)
		}
		var event orders.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, fmt.Errorf("decode PostgreSQL order event: %w", err)
		}
		normalized, err := normalizeOrderEvent(event)
		if err != nil {
			return nil, err
		}
		result = append(result, normalized)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PostgreSQL order events: %w", err)
	}
	return result, nil
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
	if repository == nil {
		return nil
	}
	repository.leaseMu.Lock()
	if repository.leaseConn != nil {
		var unlocked bool
		unlockContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = repository.leaseConn.QueryRow(unlockContext,
			`SELECT pg_advisory_unlock($1)`, repository.leaseKey).Scan(&unlocked)
		cancel()
		repository.leaseConn.Release()
		repository.leaseConn = nil
	}
	repository.leaseMu.Unlock()
	if repository.pool != nil {
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
var _ ExecutionLeaser = (*PostgresRepository)(nil)
