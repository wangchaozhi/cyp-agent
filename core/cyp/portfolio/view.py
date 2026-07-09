"""组合视图：跨场所聚合持仓，供组合级风控（总敞口 / 单标的 / 相关性簇）。"""

from __future__ import annotations

from collections.abc import Callable
from decimal import Decimal

from cyp.contracts import Position
from cyp.portfolio.correlation import CorrelationModel


async def aggregate_positions(venues) -> list[Position]:
    """把多个场所的持仓合并（单场所失败隔离，不阻断整体）。"""
    out: list[Position] = []
    for v in venues:
        try:
            out.extend(await v.positions())
        except Exception:  # noqa: BLE001
            continue
    return out


class PortfolioView:
    def __init__(self, positions: list[Position], corr: CorrelationModel | None = None) -> None:
        self.positions = positions
        self.corr = corr or CorrelationModel()

    def _price(self, symbol: str, price_of: Callable[[str], Decimal | None] | None) -> Decimal:
        if price_of:
            px = price_of(symbol)
            if px:
                return px
        # 缺实时价时用各仓入场价近似
        for p in self.positions:
            if p.symbol == symbol:
                return p.entry_price
        return Decimal(0)

    def gross_notional(self, price_of: Callable[[str], Decimal] | None = None) -> Decimal:
        return sum((p.notional_at(self._price(p.symbol, price_of)) for p in self.positions), Decimal(0))

    def symbol_notional(self, symbol: str, price_of: Callable[[str], Decimal] | None = None) -> Decimal:
        return sum((p.notional_at(self._price(p.symbol, price_of))
                    for p in self.positions if p.symbol == symbol), Decimal(0))

    def symbol_breakdown(self, price_of: Callable[[str], Decimal] | None = None) -> list[dict]:
        """按标的聚合多/空名义敞口（供仪表盘热力图）。"""
        out: dict[str, dict] = {}
        for p in self.positions:
            notional = p.notional_at(self._price(p.symbol, price_of))
            entry = out.setdefault(p.symbol, {
                "symbol": p.symbol, "cluster": self.corr.cluster_of(p.symbol),
                "long": Decimal(0), "short": Decimal(0),
            })
            entry[p.side] += notional
        return sorted(out.values(), key=lambda e: e["long"] + e["short"], reverse=True)

    def cluster_net_directional(self, cluster: str, side: str,
                                price_of: Callable[[str], Decimal] | None = None) -> Decimal:
        """相关性簇内、与 side 同向的净名义敞口（同向 +、反向 -，下限 0）。

        用于限制"在一篮子高相关资产上押过重同向"的系统性风险。
        """
        net = Decimal(0)
        for p in self.positions:
            if self.corr.cluster_of(p.symbol) != cluster:
                continue
            sign = Decimal(1) if p.side == side else Decimal(-1)
            net += sign * p.notional_at(self._price(p.symbol, price_of))
        return max(Decimal(0), net)
