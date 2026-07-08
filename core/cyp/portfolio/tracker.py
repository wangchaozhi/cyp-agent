"""PortfolioTracker：账户风险状态的单一来源。

编排器每轮用它填充 RiskContext 的回撤/连亏/下单频率——否则这些字段恒为 0，
回撤熔断、连亏冷静、频率上限等护栏在实盘循环里永远不会触发。

回撤口径（M2 近似，够触发熔断）：
- 总回撤 = (净值高水位 - 当前净值) / 高水位（含未实现，随持仓浮亏上升）。
- 日/周回撤 = 窗口内已实现亏损 / 起始净值。
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from decimal import Decimal


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


@dataclass
class _Closed:
    pnl: Decimal
    ts: datetime


@dataclass
class PortfolioTracker:
    starting_equity: Decimal | None = None
    _hwm: Decimal | None = None
    _orders: list[datetime] = field(default_factory=list)
    _closed: list[_Closed] = field(default_factory=list)
    consecutive_losses: int = 0
    realized_pnl: Decimal = Decimal(0)

    # ---- 更新 --------------------------------------------------------------

    def update_equity(self, equity: Decimal) -> None:
        if self.starting_equity is None:
            self.starting_equity = equity
        self._hwm = equity if self._hwm is None else max(self._hwm, equity)

    def record_order(self, now: datetime | None = None) -> None:
        self._orders.append(now or _utcnow())

    def record_close(self, pnl_quote: Decimal, now: datetime | None = None) -> None:
        self.realized_pnl += pnl_quote
        self._closed.append(_Closed(pnl_quote, now or _utcnow()))
        if pnl_quote < 0:
            self.consecutive_losses += 1
        else:
            self.consecutive_losses = 0

    # ---- 查询 --------------------------------------------------------------

    def orders_last_hour(self, now: datetime | None = None) -> int:
        cutoff = (now or _utcnow()) - timedelta(hours=1)
        return sum(1 for ts in self._orders if ts >= cutoff)

    def total_drawdown(self, equity: Decimal) -> Decimal:
        if not self._hwm or self._hwm <= 0:
            return Decimal(0)
        return max(Decimal(0), (self._hwm - equity) / self._hwm)

    def _window_loss_frac(self, hours: int, now: datetime | None = None) -> Decimal:
        if not self.starting_equity or self.starting_equity <= 0:
            return Decimal(0)
        cutoff = (now or _utcnow()) - timedelta(hours=hours)
        pnl = sum((c.pnl for c in self._closed if c.ts >= cutoff), Decimal(0))
        loss = -pnl if pnl < 0 else Decimal(0)
        return loss / self.starting_equity

    def daily_drawdown(self, now: datetime | None = None) -> Decimal:
        return self._window_loss_frac(24, now)

    def weekly_drawdown(self, now: datetime | None = None) -> Decimal:
        return self._window_loss_frac(24 * 7, now)

    def risk_snapshot(self, equity: Decimal, now: datetime | None = None) -> dict:
        """供编排器构造 RiskContext。"""
        now = now or _utcnow()
        return {
            "orders_last_hour": self.orders_last_hour(now),
            "consecutive_losses": self.consecutive_losses,
            "daily_drawdown": self.daily_drawdown(now),
            "weekly_drawdown": self.weekly_drawdown(now),
            "total_drawdown": self.total_drawdown(equity),
        }
