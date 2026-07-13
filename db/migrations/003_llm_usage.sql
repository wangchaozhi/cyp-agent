-- Provider-neutral model-call accounting. Detailed events retain only safe
-- metadata (never prompts or responses); daily aggregates are kept long-term.

CREATE TABLE IF NOT EXISTS llm_usage_events (
    id TEXT NOT NULL,
    ts TIMESTAMPTZ NOT NULL,
    run_id TEXT NOT NULL DEFAULT '',
    symbol TEXT NOT NULL DEFAULT '',
    agent TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    operation TEXT NOT NULL,
    status TEXT NOT NULL,
    input_tokens BIGINT NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens BIGINT NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cost_usd NUMERIC NOT NULL DEFAULT 0 CHECK (cost_usd >= 0),
    duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    token_estimated BOOLEAN NOT NULL DEFAULT FALSE,
    cost_estimated BOOLEAN NOT NULL DEFAULT FALSE,
    error_kind TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, ts)
);

SELECT create_hypertable('llm_usage_events', 'ts', if_not_exists => TRUE, migrate_data => TRUE);
CREATE INDEX IF NOT EXISTS llm_usage_events_ts_idx ON llm_usage_events (ts DESC);
CREATE INDEX IF NOT EXISTS llm_usage_events_run_idx ON llm_usage_events (run_id, ts DESC);
CREATE INDEX IF NOT EXISTS llm_usage_events_dimensions_idx
    ON llm_usage_events (provider, model, agent, symbol, ts DESC);

CREATE TABLE IF NOT EXISTS llm_usage_daily (
    day DATE NOT NULL,
    timezone TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    agent TEXT NOT NULL,
    symbol TEXT NOT NULL,
    source TEXT NOT NULL,
    status TEXT NOT NULL,
    calls BIGINT NOT NULL DEFAULT 0,
    successes BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cost_usd NUMERIC NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (day, timezone, provider, model, agent, symbol, source, status)
);

INSERT INTO schema_migrations(version) VALUES (3)
ON CONFLICT (version) DO NOTHING;
