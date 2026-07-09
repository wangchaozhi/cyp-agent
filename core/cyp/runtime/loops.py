"""三条循环：机会扫描（找新仓）+ 持仓监控（常驻高频）+ Watchdog。

- OpportunityScanner：按周期对 watchlist 逐个 run_once。
- PositionMonitor：只盯已有持仓，校验保护覆盖/健康，可触发防御性动作（M0 报告+告警）。
- 循环均支持 max_cycles / stop 事件，便于测试与优雅停机。
"""

from __future__ import annotations

import asyncio
from decimal import Decimal

from cyp.events import EventBus
from cyp.observability import get_logger


class OpportunityScanner:
    def __init__(self, orchestrator, symbols: list[str], interval: float = 300,
                 events: EventBus | None = None) -> None:
        self.orch = orchestrator
        self.symbols = symbols
        self.interval = interval
        self.events = events
        self.log = get_logger("scanner")

    async def run(self, max_cycles: int | None = None, stop: asyncio.Event | None = None) -> None:
        cycle = 0
        while not (stop and stop.is_set()):
            for symbol in self.symbols:
                if stop and stop.is_set():
                    return
                await self.orch.run_once(symbol)
            cycle += 1
            if max_cycles is not None and cycle >= max_cycles:
                return
            await asyncio.sleep(self.interval)


class PositionMonitor:
    """常驻高频，盯已有持仓：保护覆盖、止损/爆仓逼近、异常波动、保证金健康度。

    M6：告警走 Alerter（控制台 + webhook），不再只写日志。平仓执行仍留实盘阶段。
    """

    def __init__(self, venue, interval: float = 15, events: EventBus | None = None,
                 alerter=None,
                 stop_proximity: Decimal = Decimal("0.3"),       # 剩余止损距离 <30% 预警
                 liq_proximity: Decimal = Decimal("0.10"),       # 距爆仓价 <10% 预警
                 move_threshold: Decimal = Decimal("0.05"),      # 两次巡检间 >5% 波动预警
                 min_margin_ratio: Decimal = Decimal("0.05")) -> None:
        self.venue = venue
        self.interval = interval
        self.events = events
        self.alerter = alerter
        self.stop_proximity = stop_proximity
        self.liq_proximity = liq_proximity
        self.move_threshold = move_threshold
        self.min_margin_ratio = min_margin_ratio
        self._last_marks: dict[str, Decimal] = {}
        self.log = get_logger("monitor")

    async def _mark(self, symbol: str) -> Decimal | None:
        try:
            return await self.venue.fetch_ticker(symbol)
        except Exception:  # noqa: BLE001
            return None

    def _position_alerts(self, p, mark: Decimal | None, protective) -> list[str]:
        alerts: list[str] = []
        if mark is None or mark <= 0:
            return alerts
        # 异常波动：两次巡检间价格突变
        last = self._last_marks.get(p.symbol)
        if last and last > 0:
            move = abs(mark - last) / last
            if move >= self.move_threshold:
                alerts.append(f"{p.symbol} 异常波动 {move:.1%}（{last} → {mark}）")
        self._last_marks[p.symbol] = mark
        # 止损逼近：剩余距离占（入场→止损）全程的比例低于阈值
        for order in protective or []:
            if order.kind != "stop_loss":
                continue
            full = abs(p.entry_price - order.trigger_price)
            if full <= 0:
                continue
            remaining = abs(mark - order.trigger_price) / full
            if remaining <= self.stop_proximity:
                alerts.append(f"{p.symbol} 价格 {mark} 已走完 {(1 - remaining):.0%} 止损距离"
                              f"（止损 {order.trigger_price}）")
        # 爆仓逼近（合约）
        if p.instrument == "perp" and p.liq_price is not None and p.liq_price > 0:
            dist = abs(mark - p.liq_price) / mark
            if dist <= self.liq_proximity:
                alerts.append(f"{p.symbol} 价格 {mark} 距爆仓价 {p.liq_price} 仅 {dist:.2%}")
        return alerts

    async def check_once(self) -> dict:
        positions = await self.venue.positions()
        alerts: list[str] = []
        native = getattr(self.venue, "caps", None) and self.venue.caps.native_protective_orders
        if positions and not native:
            alerts += [f"{p.symbol} 无原生保护单，保护依赖监控存活" for p in positions]

        protective_of = getattr(self.venue, "protective_for", None)
        for p in positions:
            mark = await self._mark(p.symbol)
            protective = protective_of(p.symbol) if protective_of else []
            alerts += self._position_alerts(p, mark, protective)

        # 保证金健康度（合约总名义 vs 净值）
        perp_notional = sum((p.notional_at(self._last_marks.get(p.symbol, p.entry_price))
                             for p in positions if p.instrument == "perp"), Decimal(0))
        if perp_notional > 0:
            try:
                bal = await self.venue.balances()
                equity = bal.total_quote if bal.total_quote > 0 else bal.free_quote
                ratio = equity / perp_notional
                if ratio < self.min_margin_ratio * 2:
                    alerts.append(f"保证金率 {ratio:.2%} 逼近下限 {self.min_margin_ratio:.0%}")
            except Exception:  # noqa: BLE001
                pass

        report = {"positions": [p.model_dump(mode="json") for p in positions], "alerts": alerts}
        if self.events:
            await self.events.publish("position_monitor", "-", **report)
        if alerts:
            self.log.warning("position_alerts", alerts=alerts)
            if self.alerter:
                await self.alerter.alert("warning", "position_monitor", alerts=alerts)
        return report

    async def run(self, max_cycles: int | None = None, stop: asyncio.Event | None = None) -> None:
        cycle = 0
        while not (stop and stop.is_set()):
            await self.check_once()
            cycle += 1
            if max_cycles is not None and cycle >= max_cycles:
                return
            await asyncio.sleep(self.interval)
