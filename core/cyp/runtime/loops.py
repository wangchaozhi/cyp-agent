"""三条循环：机会扫描（找新仓）+ 持仓监控（常驻高频）+ Watchdog。

- OpportunityScanner：按周期对 watchlist 逐个 run_once。
- PositionMonitor：只盯已有持仓，校验保护覆盖/健康，可触发防御性动作（M0 报告+告警）。
- 循环均支持 max_cycles / stop 事件，便于测试与优雅停机。
"""

from __future__ import annotations

import asyncio

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
    """常驻高频，盯已有持仓。M0：报告 + 保护覆盖告警；平仓/止损执行在实盘阶段接。"""

    def __init__(self, venue, interval: float = 15, events: EventBus | None = None) -> None:
        self.venue = venue
        self.interval = interval
        self.events = events
        self.log = get_logger("monitor")

    async def check_once(self) -> dict:
        positions = await self.venue.positions()
        alerts: list[str] = []
        native = getattr(self.venue, "caps", None) and self.venue.caps.native_protective_orders
        if positions and not native:
            alerts = [f"{p.symbol} 无原生保护单，保护依赖监控存活" for p in positions]
        report = {"positions": [p.model_dump(mode="json") for p in positions], "alerts": alerts}
        if self.events:
            await self.events.publish("position_monitor", "-", **report)
        if alerts:
            self.log.warning("position_alerts", alerts=alerts)
        return report

    async def run(self, max_cycles: int | None = None, stop: asyncio.Event | None = None) -> None:
        cycle = 0
        while not (stop and stop.is_set()):
            await self.check_once()
            cycle += 1
            if max_cycles is not None and cycle >= max_cycles:
                return
            await asyncio.sleep(self.interval)
