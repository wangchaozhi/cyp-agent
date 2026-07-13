-- cyp-agent 初始化（仅数据卷首次创建时执行；应用侧建表同样幂等，可重复运行）。
-- OHLCV 走 TimescaleDB hypertable（按 ts 分区），为历史行情时序查询做准备。

CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS lessons (
    id     BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    symbol TEXT NOT NULL DEFAULT '',
    text   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS checkpoints (
    run_id TEXT NOT NULL,
    step   TEXT NOT NULL,
    data   TEXT NOT NULL,
    PRIMARY KEY (run_id, step)
);

CREATE TABLE IF NOT EXISTS ohlcv (
    venue     TEXT NOT NULL,
    symbol    TEXT NOT NULL,
    timeframe TEXT NOT NULL,
    ts        TIMESTAMPTZ NOT NULL,
    open      NUMERIC NOT NULL,
    high      NUMERIC NOT NULL,
    low       NUMERIC NOT NULL,
    close     NUMERIC NOT NULL,
    volume    NUMERIC NOT NULL,
	 ingested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	 quality_status TEXT NOT NULL DEFAULT 'validated',
    PRIMARY KEY (venue, symbol, timeframe, ts)
);

SELECT create_hypertable('ohlcv', 'ts', if_not_exists => TRUE, migrate_data => TRUE);
CREATE INDEX IF NOT EXISTS ohlcv_symbol_timeframe_ts_idx
    ON ohlcv (symbol, timeframe, ts DESC);

INSERT INTO schema_migrations(version) VALUES (1), (2)
ON CONFLICT (version) DO NOTHING;
