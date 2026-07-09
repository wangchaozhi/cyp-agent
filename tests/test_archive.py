"""OHLCV 归档：假 venue 离线验证分页拉取 + SQLite 增量缓存。"""

import asyncio
from datetime import datetime, timedelta, timezone
from decimal import Decimal

from cyp.backtest import OhlcvArchive, timeframe_delta
from cyp.contracts import Candle

run = asyncio.run


class FakeHistoryVenue:
    id = "fake"

    def __init__(self, bars: int = 600) -> None:
        now = datetime.now(timezone.utc).replace(minute=0, second=0, microsecond=0)
        self.candles = [
            Candle(ts=now - timedelta(hours=bars - i),
                   open=Decimal(100 + i), high=Decimal(101 + i),
                   low=Decimal(99 + i), close=Decimal(100 + i), volume=Decimal(10))
            for i in range(bars)
        ]
        self.calls = 0

    async def fetch_ohlcv(self, symbol, timeframe="1h", limit=200, since=None):
        self.calls += 1
        rows = [c for c in self.candles if since is None or c.ts >= since]
        return rows[:limit]


def test_timeframe_delta():
    assert timeframe_delta("1h") == timedelta(hours=1)
    assert timeframe_delta("15m") == timedelta(minutes=15)
    try:
        timeframe_delta("3w")
        raise AssertionError("应抛 ValueError")
    except ValueError:
        pass


def test_ensure_paginates_and_caches(tmp_path):
    venue = FakeHistoryVenue(bars=600)
    archive = OhlcvArchive(str(tmp_path / "ohlcv.sqlite"))
    candles = run(archive.ensure(venue, "BTC/USDT", "1h", bars=500))
    assert len(candles) == 500
    assert candles[0].ts < candles[-1].ts                      # 升序
    assert venue.calls >= 1

    # 二次调用：缓存已足量，不再触网
    calls_before = venue.calls
    again = run(archive.ensure(venue, "BTC/USDT", "1h", bars=500))
    assert len(again) == 500
    assert venue.calls == calls_before


def test_load_reads_persisted_cache(tmp_path):
    db = str(tmp_path / "ohlcv.sqlite")
    venue = FakeHistoryVenue(bars=300)
    run(OhlcvArchive(db).ensure(venue, "ETH/USDT", "1h", bars=200))

    fresh = OhlcvArchive(db)                                    # 新实例读同一库
    cached = run(fresh.load("fake", "ETH/USDT", "1h", bars=200))
    assert len(cached) == 200
    assert cached[-1].close > cached[0].close                   # 单调序列被完整还原


def test_ensure_incremental_topup(tmp_path):
    db = str(tmp_path / "ohlcv.sqlite")
    venue = FakeHistoryVenue(bars=600)
    first = run(OhlcvArchive(db).ensure(venue, "BTC/USDT", "1h", bars=100))
    assert len(first) == 100
    more = run(OhlcvArchive(db).ensure(venue, "BTC/USDT", "1h", bars=400))
    assert len(more) == 400
