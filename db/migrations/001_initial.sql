-- cyp-agent Go backend baseline schema.
-- Monetary values use NUMERIC; secrets and raw authorization material never
-- belong in this database.
--
-- Tables actively used by the current release (PostgresRepository):
--   schema_migrations, lessons, checkpoints, ohlcv (backtest archive).
-- The remaining tables (runs, approvals, orders, order_events,
-- runtime_controls, reconciliations, audit_events) are reserved for the G4
-- persistent order state machine (docs/ROADMAP.md) and stay empty until the
-- code that writes them ships.

CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS lessons (
    id BIGSERIAL PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT now(),
    symbol TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS checkpoints (
    run_id TEXT NOT NULL,
    step TEXT NOT NULL,
    data TEXT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, step)
);

CREATE TABLE IF NOT EXISTS ohlcv (
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
);

SELECT create_hypertable('ohlcv', 'ts', if_not_exists => TRUE, migrate_data => TRUE);
CREATE INDEX IF NOT EXISTS ohlcv_symbol_timeframe_ts_idx
    ON ohlcv (symbol, timeframe, ts DESC);

CREATE TABLE IF NOT EXISTS runs (
    run_id TEXT PRIMARY KEY,
    symbol TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    result JSONB
);

CREATE TABLE IF NOT EXISTS approvals (
    run_id TEXT PRIMARY KEY REFERENCES runs(run_id) ON DELETE CASCADE,
    decision TEXT,
    operator TEXT,
    note TEXT NOT NULL DEFAULT '',
    proposal JSONB NOT NULL,
    assessment JSONB NOT NULL,
    decided_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS orders (
    client_id TEXT PRIMARY KEY,
    run_id TEXT REFERENCES runs(run_id),
    venue TEXT NOT NULL,
    symbol TEXT NOT NULL,
    status TEXT NOT NULL,
    intent JSONB NOT NULL,
    result JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS order_events (
    id BIGSERIAL PRIMARY KEY,
    client_id TEXT NOT NULL REFERENCES orders(client_id),
    ts TIMESTAMPTZ NOT NULL DEFAULT now(),
    type TEXT NOT NULL,
    payload JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS order_events_client_ts_idx ON order_events (client_id, ts);

CREATE TABLE IF NOT EXISTS runtime_controls (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS reconciliations (
    id BIGSERIAL PRIMARY KEY,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    ok BOOLEAN NOT NULL DEFAULT FALSE,
    report JSONB,
    error TEXT
);

CREATE TABLE IF NOT EXISTS audit_events (
    id BIGSERIAL PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT now(),
    request_id TEXT,
    run_id TEXT,
    operator TEXT,
    action TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb
);

INSERT INTO schema_migrations(version) VALUES (1)
ON CONFLICT (version) DO NOTHING;
