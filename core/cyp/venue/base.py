"""Venue 统一抽象：让「交易员」面对 CEX / 链上 / 模拟盘长得一样。

差异（保护单参数、持仓/保证金模式、gas/授权等）由各实现内部消化；
上层只依赖此协议。新增场所 = 实现 Venue + 注册一行。
"""

from __future__ import annotations

from dataclasses import dataclass, field
from decimal import Decimal
from typing import Literal, Protocol, runtime_checkable

from cyp.contracts import (
    Balances,
    Candle,
    ExecutionResult,
    OrderBook,
    OrderIntent,
    Position,
)

VenueKind = Literal["cex", "onchain", "paper"]


@dataclass
class VenueCaps:
    spot: bool = True
    perp: bool = False
    native_protective_orders: bool = False   # 是否支持交易所侧原生止损/止盈
    read_only: bool = False                  # True = 只读行情，不可下单


@dataclass
class PreflightReport:
    """下单前体检：估算成交价/滑点/爆仓价，供风控引擎做最终硬校验。"""

    ok: bool
    est_price: Decimal
    est_slippage_bps: Decimal | None = None
    est_liq_price: Decimal | None = None      # 合约爆仓价估算
    est_price_impact: Decimal | None = None   # 链上价格冲击
    reasons: list[str] = field(default_factory=list)


@runtime_checkable
class Venue(Protocol):
    id: str
    kind: VenueKind
    caps: VenueCaps

    def is_configured(self) -> bool: ...

    # 行情（只读，通常无需密钥）
    async def fetch_ticker(self, symbol: str) -> Decimal: ...
    async def fetch_ohlcv(self, symbol: str, timeframe: str = "1h", limit: int = 200) -> list[Candle]: ...
    async def fetch_orderbook(self, symbol: str, depth: int = 20) -> OrderBook: ...

    # 账户
    async def positions(self) -> list[Position]: ...
    async def balances(self) -> Balances: ...

    # 执行
    async def preflight(self, intent: OrderIntent) -> PreflightReport: ...
    async def place(self, intent: OrderIntent) -> ExecutionResult: ...  # 幂等（client_id）
    async def cancel(self, order_id: str) -> None: ...
