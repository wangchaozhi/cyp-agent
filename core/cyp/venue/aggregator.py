"""跨所行情聚合：多场所报价 → 最优执行场所 + 跨所价差（套利线索，仅提示不自动执行）。"""

from __future__ import annotations

from decimal import Decimal


class MarketAggregator:
    def __init__(self, venues: list) -> None:
        self.venues = venues

    async def tickers(self, symbol: str) -> dict[str, Decimal]:
        """各场所最新价（单场所失败隔离，跳过）。"""
        out: dict[str, Decimal] = {}
        for v in self.venues:
            try:
                out[v.id] = await v.fetch_ticker(symbol)
            except Exception:  # noqa: BLE001
                continue
        return out

    async def best_venue(self, symbol: str, side: str) -> tuple[str | None, Decimal | None]:
        """买/做多取最低价场所，卖/做空取最高价场所。"""
        t = await self.tickers(symbol)
        if not t:
            return None, None
        vid = min(t, key=t.get) if side in ("long", "buy") else max(t, key=t.get)
        return vid, t[vid]

    async def spread_bps(self, symbol: str) -> Decimal | None:
        """跨所价差（bps）——同一标的在不同场所的价格离散度，套利/异常线索。"""
        t = await self.tickers(symbol)
        if len(t) < 2:
            return None
        lo, hi = min(t.values()), max(t.values())
        return (hi - lo) / lo * Decimal(10000) if lo > 0 else None
