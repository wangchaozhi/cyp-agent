"""RuntimeEngine：编排三条循环 + 启动对账（对账未完成冻结开仓）。

    engine = build_engine(settings, orchestrator, venue)
    await engine.startup_reconcile()   # 先对账
    await engine.start()               # 常驻双循环
    ...
    await engine.stop()

测试用 run_bounded(scan_cycles, monitor_cycles) 跑有限轮次。
"""

from __future__ import annotations

import asyncio
from decimal import Decimal

from cyp.config import Settings
from cyp.events import EventBus
from cyp.observability import get_logger
from cyp.runtime.loops import OpportunityScanner, PositionMonitor
from cyp.runtime.reconcile import Reconciler, ReconcileReport


class RuntimeEngine:
    def __init__(self, orchestrator, venue, symbols: list[str], memory=None,
                 events: EventBus | None = None, scan_interval: float = 300,
                 monitor_interval: float = 15) -> None:
        self.orch = orchestrator
        self.events = events
        self.reconciler = Reconciler(venue, memory, events)
        self.scanner = OpportunityScanner(orchestrator, symbols, scan_interval, events)
        min_margin = getattr(getattr(orchestrator, "settings", None), "risk", None)
        self.monitor = PositionMonitor(
            venue, monitor_interval, events,
            alerter=getattr(orchestrator, "alerter", None),
            min_margin_ratio=min_margin.min_margin_ratio if min_margin else Decimal("0.05"),
        )
        self.log = get_logger("runtime")
        self._stop = asyncio.Event()
        self._tasks: list[asyncio.Task] = []

    async def startup_reconcile(self) -> ReconcileReport:
        # 对账期间冻结新开仓（风控引擎读 orch.reconciling）
        self.orch.reconciling = True
        try:
            report = await self.reconciler.reconcile()
            return report
        finally:
            self.orch.reconciling = False

    async def start(self) -> None:
        await self.startup_reconcile()
        self._stop.clear()
        self._tasks = [
            asyncio.create_task(self.scanner.run(stop=self._stop)),
            asyncio.create_task(self.monitor.run(stop=self._stop)),
        ]
        self.log.info("runtime_started", symbols=self.scanner.symbols)

    async def stop(self) -> None:
        self._stop.set()
        for t in self._tasks:
            t.cancel()
        await asyncio.gather(*self._tasks, return_exceptions=True)
        self._tasks = []
        self.log.info("runtime_stopped")

    async def run_bounded(self, scan_cycles: int = 1, monitor_cycles: int = 1) -> ReconcileReport:
        # bounded/测试模式不按真实周期睡眠，直接连跑
        saved = (self.scanner.interval, self.monitor.interval)
        self.scanner.interval, self.monitor.interval = 0, 0
        try:
            report = await self.startup_reconcile()
            await self.scanner.run(max_cycles=scan_cycles)
            await self.monitor.run(max_cycles=monitor_cycles)
            return report
        finally:
            self.scanner.interval, self.monitor.interval = saved


def build_engine(settings: Settings, orchestrator, venue, events: EventBus | None = None,
                 memory=None) -> RuntimeEngine:
    return RuntimeEngine(
        orchestrator=orchestrator, venue=venue, symbols=settings.watchlist_symbols(),
        memory=memory, events=events,
        scan_interval=settings.scan_interval, monitor_interval=settings.monitor_interval,
    )
