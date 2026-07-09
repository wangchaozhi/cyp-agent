"""行情源：把原始行情组装成 MarketSnapshot 喂给分析师团。

两种实现：
- CexMarketData：从只读 CexVenue 拉真实行情（需网络，无需密钥）。
- SyntheticMarketData：确定性随机游走，零网络零密钥——保证「无密钥可端到端跑通」，
  也是单测与离线 demo 的默认。

降级：缺失的维度（衍生品/链上/情绪）留 None，分析师据此标 degraded。
"""

from __future__ import annotations

import asyncio
import random
from datetime import datetime, timedelta, timezone
from decimal import Decimal
from typing import Protocol

from cyp.contracts import (
    Candle,
    DerivativesData,
    MarketSnapshot,
    OrderBook,
    SentimentData,
)


class MarketDataSource(Protocol):
    async def snapshot(self, symbol: str) -> MarketSnapshot: ...


class CexMarketData:
    """真实只读行情（Binance/OKX 等）。

    - K 线 + 盘口：所有标的。
    - 衍生品维度（资金费/OI/多空比）：仅 perp 标的（symbol 含 ':'），并发拉取、失败隔离；
      资金费拉不到则整段留 None，衍生品分析师按降级处理。
    - 情绪/链上：需外部 API（CYP_SENTIMENT_API 等），此处不组装。
    """

    def __init__(self, venue) -> None:
        self.venue = venue

    async def snapshot(self, symbol: str) -> MarketSnapshot:
        candles = await self.venue.fetch_ohlcv(symbol, "1h", 200)
        try:
            ob: OrderBook | None = await self.venue.fetch_orderbook(symbol)
        except Exception:
            ob = None
        derivatives = await self._derivatives(symbol) if ":" in symbol else None
        return MarketSnapshot(symbol=symbol, venue=self.venue.id, ohlcv=candles,
                              orderbook=ob, derivatives=derivatives)

    async def _derivatives(self, symbol: str) -> DerivativesData | None:
        if not hasattr(self.venue, "fetch_funding_rate"):
            return None

        async def _get(name: str):
            fn = getattr(self.venue, name, None)
            return await fn(symbol) if fn else None

        funding, oi, lsr = await asyncio.gather(
            _get("fetch_funding_rate"), _get("fetch_open_interest"),
            _get("fetch_long_short_ratio"), return_exceptions=True)
        funding = None if isinstance(funding, BaseException) else funding
        if funding is None:      # 分析师以资金费为主信号，缺失即视为无衍生品数据
            return None
        return DerivativesData(
            funding_rate=funding,
            open_interest=None if isinstance(oi, BaseException) else oi,
            long_short_ratio=None if isinstance(lsr, BaseException) else lsr)


class SyntheticMarketData:
    """合成行情（随机游走）。默认同 seed 同输出；live_ticks=True 时最新价随请求推进。"""

    def __init__(self, base: Decimal = Decimal("60000"), bars: int = 200,
                 seed: int = 7, vol: float = 0.01, drift: float = 0.0005,
                 live_ticks: bool = False) -> None:
        self.base = base
        self.bars = bars
        self.seed = seed
        self.vol = vol
        self.drift = drift
        self.live_ticks = live_ticks
        self._ticks: dict[str, int] = {}
        self._marks: dict[str, float] = {}

    async def snapshot(self, symbol: str) -> MarketSnapshot:
        rng = random.Random(f"{self.seed}:{symbol}")
        price = float(self.base)
        now = datetime.now(timezone.utc)
        candles: list[Candle] = []
        for i in range(self.bars):
            ret = self.drift + rng.gauss(0, self.vol)
            open_p = price
            close_p = max(0.01, price * (1 + ret))
            high_p = max(open_p, close_p) * (1 + abs(rng.gauss(0, self.vol / 2)))
            low_p = min(open_p, close_p) * (1 - abs(rng.gauss(0, self.vol / 2)))
            vol = abs(rng.gauss(1000, 200))
            ts = now - timedelta(hours=(self.bars - i))
            candles.append(Candle(
                ts=ts, open=Decimal(str(round(open_p, 2))), high=Decimal(str(round(high_p, 2))),
                low=Decimal(str(round(low_p, 2))), close=Decimal(str(round(close_p, 2))),
                volume=Decimal(str(round(vol, 4))),
            ))
            price = close_p

        if self.live_ticks and candles:
            tick = self._ticks.get(symbol, 0) + 1
            self._ticks[symbol] = tick
            tick_rng = random.Random(f"{self.seed}:{symbol}:tick:{tick}")
            mark = self._marks.get(symbol, float(candles[-1].close))
            mark = max(0.01, mark * (1 + self.drift / 24 + tick_rng.gauss(0, self.vol / 16)))
            self._marks[symbol] = mark
            prev = candles[-2].close if len(candles) > 1 else candles[-1].open
            mark_dec = Decimal(str(round(mark, 2)))
            candles[-1] = candles[-1].model_copy(update={
                "open": prev,
                "high": max(mark_dec, prev),
                "low": min(mark_dec, prev),
                "close": mark_dec,
            })

        derivatives = DerivativesData(
            funding_rate=Decimal(str(round(rng.uniform(-0.0005, 0.0005), 6))),
            open_interest=Decimal(str(round(rng.uniform(1e8, 5e8), 0))),
            long_short_ratio=Decimal(str(round(rng.uniform(0.8, 1.3), 3))),
        )
        sentiment = SentimentData(fear_greed=rng.randint(20, 80))
        return MarketSnapshot(symbol=symbol, venue="synthetic", ohlcv=candles,
                              derivatives=derivatives, sentiment=sentiment)


def build_data_source(kind: str, venue=None) -> MarketDataSource:
    """kind='synthetic'（默认，离线）或 'cex'（真实只读行情）。"""
    if kind == "cex":
        if venue is None:
            raise ValueError("cex 行情源需要传入只读 venue")
        return CexMarketData(venue)
    return SyntheticMarketData()
