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

    async def funding_rates(self, symbol: str) -> dict[str, Decimal]:
        """各场所永续资金费率（无合约/不支持的场所跳过）。

        symbol 为现货写法时自动转永续（BTC/USDT → BTC/USDT:USDT）。
        """
        perp_symbol = symbol if ":" in symbol else f"{symbol}:{symbol.split('/')[-1]}"
        out: dict[str, Decimal] = {}
        for v in self.venues:
            fetch = getattr(v, "fetch_funding_rate", None)
            if fetch is None:
                continue
            try:
                rate = await fetch(perp_symbol)
            except Exception:  # noqa: BLE001
                continue
            if rate is not None:
                out[v.id] = rate
        return out

    async def arb_hints(self, symbol: str,
                        spread_threshold_bps: Decimal = Decimal("10"),
                        funding_gap_threshold: Decimal = Decimal("0.0003")) -> list[str]:
        """套利线索（★ 仅提示，绝不自动执行）：跨所价差超阈 / 跨所资金费差超阈。"""
        hints: list[str] = []
        spread = await self.spread_bps(symbol)
        if spread is not None and spread >= spread_threshold_bps:
            t = await self.tickers(symbol)
            lo_v = min(t, key=t.get)
            hi_v = max(t, key=t.get)
            hints.append(f"{symbol} 跨所价差 {spread:.1f}bps（{lo_v} 低 / {hi_v} 高），存在搬砖空间")
        funding = await self.funding_rates(symbol)
        if len(funding) >= 2:
            lo_v = min(funding, key=funding.get)
            hi_v = max(funding, key=funding.get)
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
