"""OHLCV 归档：从 CEX 分页拉取历史 K 线 → SQLite 增量缓存，供回测使用真实历史。

- 公共行情无需 API Key（CexVenue 只读即可）。
- 缓存按 (venue, symbol, timeframe, ts) 去重，重复拉取只补缺口。
- 离线可测：venue 只需实现 fetch_ohlcv(symbol, timeframe, limit, since)。
"""

from __future__ import annotations

from datetime import datetime, timedelta, timezone
from decimal import Decimal
from pathlib import Path

import aiosqlite

from cyp.contracts import Candle

_TF_MINUTES = {"1m": 1, "5m": 5, "15m": 15, "30m": 30, "1h": 60, "4h": 240, "1d": 1440}

_SCHEMA = """
CREATE TABLE IF NOT EXISTS ohlcv (
    venue TEXT NOT NULL,
    symbol TEXT NOT NULL,
    timeframe TEXT NOT NULL,
    ts INTEGER NOT NULL,
    open TEXT NOT NULL,
    high TEXT NOT NULL,
    low TEXT NOT NULL,
    close TEXT NOT NULL,
    volume TEXT NOT NULL,
    PRIMARY KEY (venue, symbol, timeframe, ts)
)
"""


def timeframe_delta(timeframe: str) -> timedelta:
    if timeframe not in _TF_MINUTES:
        raise ValueError(f"不支持的 timeframe：{timeframe}（可选 {sorted(_TF_MINUTES)}）")
    return timedelta(minutes=_TF_MINUTES[timeframe])


class OhlcvArchive:
    def __init__(self, db_path: str = "./data/ohlcv.sqlite") -> None:
        self._path = Path(db_path)

    async def _conn(self) -> aiosqlite.Connection:
        self._path.parent.mkdir(parents=True, exist_ok=True)
        conn = await aiosqlite.connect(self._path)
        await conn.execute(_SCHEMA)
        return conn

    async def load(self, venue_id: str, symbol: str, timeframe: str = "1h",
                   bars: int = 500) -> list[Candle]:
        """读缓存中最近 bars 根 K 线（按时间升序）。"""
        conn = await self._conn()
        try:
            cur = await conn.execute(
                "SELECT ts, open, high, low, close, volume FROM ohlcv "
                "WHERE venue=? AND symbol=? AND timeframe=? ORDER BY ts DESC LIMIT ?",
                (venue_id, symbol, timeframe, bars))
            rows = await cur.fetchall()
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
                "INSERT OR REPLACE INTO ohlcv (venue, symbol, timeframe, ts, open, high, low, close, volume) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                [(venue_id, symbol, timeframe, int(c.ts.timestamp()),
                  str(c.open), str(c.high), str(c.low), str(c.close), str(c.volume))
                 for c in candles])
            await conn.commit()
        finally:
            await conn.close()

    @staticmethod
    def _to_candle(row) -> Candle:
        ts, o, hi, lo, c, v = row
        return Candle(ts=datetime.fromtimestamp(ts, tz=timezone.utc),
                      open=Decimal(o), high=Decimal(hi), low=Decimal(lo),
                      close=Decimal(c), volume=Decimal(v))
