"""跨所行情聚合：多场所报价 → 最优执行场所 + 跨所价差（套利线索，仅提示不自动执行）。"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from decimal import Decimal
from typing import Any


@dataclass(frozen=True)
class MarketSummary:
    """One coherent cross-venue sample used by the market API."""

    tickers: dict[str, Decimal]
    best_buy: tuple[str | None, Decimal | None]
    best_sell: tuple[str | None, Decimal | None]
    spread_bps: Decimal | None
    funding_rates: dict[str, Decimal]
    arb_hints: list[str]


class MarketAggregator:
    def __init__(self, venues: list[Any]) -> None:
        self.venues = venues

    async def tickers(self, symbol: str) -> dict[str, Decimal]:
        """并发拉取各场所最新价（单场所失败隔离，跳过）。"""
        async def fetch(v: Any) -> tuple[str, Decimal] | None:
            try:
                return v.id, await v.fetch_ticker(symbol)
            except Exception:  # noqa: BLE001
                return None

        results = await asyncio.gather(*(fetch(v) for v in self.venues))
        return dict(item for item in results if item is not None)

    @staticmethod
    def _best(tickers: dict[str, Decimal], side: str) -> tuple[str | None, Decimal | None]:
        if not tickers:
            return None, None
        pick = min if side in ("long", "buy") else max
        venue_id = pick(tickers, key=lambda vid: tickers[vid])
        return venue_id, tickers[venue_id]

    async def best_venue(self, symbol: str, side: str) -> tuple[str | None, Decimal | None]:
        """买/做多取最低价场所，卖/做空取最高价场所。"""
        return self._best(await self.tickers(symbol), side)

    @staticmethod
    def _spread(tickers: dict[str, Decimal]) -> Decimal | None:
        if len(tickers) < 2:
            return None
        lo, hi = min(tickers.values()), max(tickers.values())
        return (hi - lo) / lo * Decimal(10000) if lo > 0 else None

    async def spread_bps(self, symbol: str) -> Decimal | None:
        """跨所价差（bps）——同一标的在不同场所的价格离散度，套利/异常线索。"""
        return self._spread(await self.tickers(symbol))

    async def funding_rates(self, symbol: str) -> dict[str, Decimal]:
        """各场所永续资金费率（无合约/不支持的场所跳过）。

        symbol 为现货写法时自动转永续（BTC/USDT → BTC/USDT:USDT）。
        """
        perp_symbol = symbol if ":" in symbol else f"{symbol}:{symbol.split('/')[-1]}"
        async def fetch(v: Any) -> tuple[str, Decimal] | None:
            fetch = getattr(v, "fetch_funding_rate", None)
            if fetch is None:
                return None
            try:
                rate = await fetch(perp_symbol)
            except Exception:  # noqa: BLE001
                return None
            return (v.id, rate) if rate is not None else None

        results = await asyncio.gather(*(fetch(v) for v in self.venues))
        return dict(item for item in results if item is not None)

    @staticmethod
    def _hints(
        symbol: str,
        tickers: dict[str, Decimal],
        funding: dict[str, Decimal],
        spread_threshold_bps: Decimal,
        funding_gap_threshold: Decimal,
    ) -> list[str]:
        hints: list[str] = []
        spread = MarketAggregator._spread(tickers)
        if spread is not None and spread >= spread_threshold_bps:
            lo_v = min(tickers, key=lambda vid: tickers[vid])
            hi_v = max(tickers, key=lambda vid: tickers[vid])
            hints.append(f"{symbol} 跨所价差 {spread:.1f}bps（{lo_v} 低 / {hi_v} 高），存在搬砖空间")
        if len(funding) >= 2:
            lo_v = min(funding, key=lambda vid: funding[vid])
            hi_v = max(funding, key=lambda vid: funding[vid])
            gap = funding[hi_v] - funding[lo_v]
            if gap >= funding_gap_threshold:
                hints.append(f"{symbol} 跨所资金费差 {gap:.5f}（{hi_v} 收 / {lo_v} 付），"
                             "可关注费率套利（多低费所/空高费所）")
        elif len(funding) == 1:
            vid, rate = next(iter(funding.items()))
            if abs(rate) >= funding_gap_threshold:
                side = "多头付费拥挤" if rate > 0 else "空头付费拥挤"
                hints.append(f"{symbol} {vid} 资金费 {rate:.5f}（{side}），留意反转风险")
        return hints

    async def arb_hints(self, symbol: str,
                        spread_threshold_bps: Decimal = Decimal("10"),
                        funding_gap_threshold: Decimal = Decimal("0.0003")) -> list[str]:
        """套利线索（★ 仅提示，绝不自动执行）：跨所价差超阈 / 跨所资金费差超阈。"""
        tickers, funding = await asyncio.gather(self.tickers(symbol), self.funding_rates(symbol))
        return self._hints(symbol, tickers, funding, spread_threshold_bps, funding_gap_threshold)

    async def summary(
        self,
        symbol: str,
        spread_threshold_bps: Decimal = Decimal("10"),
        funding_gap_threshold: Decimal = Decimal("0.0003"),
    ) -> MarketSummary:
        """Fetch each upstream dimension once and derive a consistent market view."""
        tickers, funding = await asyncio.gather(self.tickers(symbol), self.funding_rates(symbol))
        return MarketSummary(
            tickers=tickers,
            best_buy=self._best(tickers, "long"),
            best_sell=self._best(tickers, "short"),
            spread_bps=self._spread(tickers),
            funding_rates=funding,
            arb_hints=self._hints(
                symbol, tickers, funding, spread_threshold_bps, funding_gap_threshold
            ),
        )
