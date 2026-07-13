-- Make existing OHLCV history auditable and query-efficient. Retention is
-- configurable at runtime and is enforced by the async archive worker.

ALTER TABLE ohlcv
    ADD COLUMN IF NOT EXISTS ingested_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE ohlcv
    ADD COLUMN IF NOT EXISTS quality_status TEXT NOT NULL DEFAULT 'validated';

CREATE INDEX IF NOT EXISTS ohlcv_symbol_timeframe_ts_idx
    ON ohlcv (symbol, timeframe, ts DESC);

INSERT INTO schema_migrations(version) VALUES (2)
ON CONFLICT (version) DO NOTHING;
