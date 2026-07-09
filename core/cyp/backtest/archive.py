"""OHLCV 归档：从 CEX 分页拉取历史 K 线 → PostgreSQL/TimescaleDB 增量缓存，供回测使用真实历史。

- 公共行情无需 API Key（CexVenue 只读即可）。
- 缓存按 (venue, symbol, timeframe, ts) 去重，重复拉取只补缺口。
- ohlcv 表为 TimescaleDB hypertable（按 ts 分区），为后续大规模历史行情做准备。
- 离线可测：venue 只需实现 fetch_ohlcv(symbol, timeframe, limit, since)。
"""

from __future__ import annotations

from datetime import datetime, timedelta, timezone
from decimal import Decimal

import asyncpg

from cyp.contracts import Candle
from cyp.memory.store import default_db_url

_TF_MINUTES = {"1m": 1, "5m": 5, "15m": 15, "30m": 30, "1h": 60, "4h": 240, "1d": 1440}

_SCHEMA_STMTS = (
    "CREATE EXTENSION IF NOT EXISTS timescaledb",
    """
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
        PRIMARY KEY (venue, symbol, timeframe, ts)
    )
    """,
    "SELECT create_hypertable('ohlcv', 'ts', if_not_exists => TRUE, migrate_data => TRUE)",
)

_initialized_dsns: set[str] = set()   # 每进程每 DSN 只建一次表


def timeframe_delta(timeframe: str) -> timedelta:
    if timeframe not in _TF_MINUTES:
        raise ValueError(f"不支持的 timeframe：{timeframe}（可选 {sorted(_TF_MINUTES)}）")
    return timedelta(minutes=_TF_MINUTES[timeframe])


class OhlcvArchive:
    def __init__(self, dsn: str | None = None) -> None:
        self._dsn = dsn or default_db_url()

    async def _conn(self) -> asyncpg.Connection:
        conn = await asyncpg.connect(self._dsn)
        if self._dsn not in _initialized_dsns:
            for stmt in _SCHEMA_STMTS:
                await conn.execute(stmt)
            _initialized_dsns.add(self._dsn)
        return conn

    async def load(self, venue_id: str, symbol: str, timeframe: str = "1h",
                   bars: int = 500) -> list[Candle]:
        """读缓存中最近 bars 根 K 线（按时间升序）。"""
        conn = await self._conn()
        try:
            rows = await conn.fetch(
                "SELECT ts, open, high, low, close, volume FROM ohlcv "
                "WHERE venue=$1 AND symbol=$2 AND timeframe=$3 ORDER BY ts DESC LIMIT $4",
                venue_id, symbol, timeframe, bars)
        finally:
            await conn.close()
        return [self._to_candle(r) for r in reversed(rows)]

    async def ensure(self, venue, symbol: str, timeframe: str = "1h",
                     bars: int = 500) -> list[Candle]:
        """确保缓存至少有 bars 根：不足则从 venue 分页拉取补齐，再返回最近 bars 根。"""
        venue_id = getattr(venue, "id", "cex")
        cached = await self.load(venue_id, symbol, timeframe, bars)
        if len(cached) >= bars:
            return cached[-bars:]

        step = timeframe_delta(timeframe)
        now = datetime.now(timezone.utc)
        since = now - step * (bars + 2)   # +2 根缓冲：对齐 K 线边界，防非整点时刻少取
        if cached:   # 已有一段历史：从最后一根之后增量补
            since = min(since, cached[-1].ts + step)

        fetched: dict[datetime, Candle] = {c.ts: c for c in cached}
        while len(fetched) < bars * 2:   # 上限护栏，防交易所返回异常数据死循环
            batch = await venue.fetch_ohlcv(symbol, timeframe=timeframe,
                                            limit=min(500, bars), since=since)
            if not batch:
                break
            new = [c for c in batch if c.ts not in fetched]
            for c in batch:
                fetched[c.ts] = c
            last_ts = batch[-1].ts
            if not new or last_ts + step > now:
                break
            since = last_ts + step

        candles = sorted(fetched.values(), key=lambda c: c.ts)
        await self._save(venue_id, symbol, timeframe, candles)
        return candles[-bars:]

    async def _save(self, venue_id: str, symbol: str, timeframe: str,
                    candles: list[Candle]) -> None:
        if not candles:
            return
        conn = await self._conn()
        try:
            await conn.executemany(
                "INSERT INTO ohlcv (venue, symbol, timeframe, ts, open, high, low, close, volume) "
                "VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) "
                "ON CONFLICT (venue, symbol, timeframe, ts) DO UPDATE SET "
                "open=EXCLUDED.open, high=EXCLUDED.high, low=EXCLUDED.low, "
                "close=EXCLUDED.close, volume=EXCLUDED.volume",
                [(venue_id, symbol, timeframe, c.ts,
                  c.open, c.high, c.low, c.close, c.volume) for c in candles])
        finally:
            await conn.close()

    @staticmethod
    def _to_candle(row) -> Candle:
        ts, o, hi, lo, c, v = row
        return Candle(ts=ts, open=Decimal(o), high=Decimal(hi), low=Decimal(lo),
                      close=Decimal(c), volume=Decimal(v))
