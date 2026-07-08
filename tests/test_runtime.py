"""运行时：对账门冻结开仓 / 扫描循环 / 监控 / 引擎端到端。离线。"""

import asyncio
from datetime import datetime, timezone
from decimal import Decimal

from cyp.approval import AutoApprove
from cyp.config import Settings
from cyp.contracts import Candle, DerivativesData, MarketSnapshot, SentimentData
from cyp.events import EventBus
from cyp.orchestrator import Orchestrator
from cyp.runtime import OpportunityScanner, PositionMonitor, build_engine
from cyp.venue import PaperVenue

run = asyncio.run


class UptrendData:
    async def snapshot(self, symbol: str) -> MarketSnapshot:
        candles = [Candle(ts=datetime.now(timezone.utc), open=Decimal(50000 + i * 125),
                          high=Decimal(50000 + i * 125 + 50), low=Decimal(50000 + i * 125 - 50),
                          close=Decimal(50000 + i * 125), volume=Decimal("100")) for i in range(80)]
        return MarketSnapshot(symbol=symbol, venue="synthetic", ohlcv=candles,
                              derivatives=DerivativesData(funding_rate=Decimal("-0.0005"),
                                                          long_short_ratio=Decimal("0.8")),
                              sentiment=SentimentData(fear_greed=20))


def _orch(venue=None, approval=None):
    return Orchestrator(Settings(_env_file=None), UptrendData(), venue or PaperVenue(),
                        approval=approval or AutoApprove())


def test_reconciling_freezes_new_open():
    orch = _orch()
    orch.reconciling = True                    # 对账未完成
    res = run(orch.run_once("BTC/USDT"))
    assert res.status == "rejected"
    assert any("reconciling" in v for v in res.assessment.hard_violations)


def test_startup_reconcile_toggles_flag_off():
    orch = _orch()
    engine = build_engine(Settings(_env_file=None), orch, orch.venue)
    report = run(engine.startup_reconcile())
    assert report.ok and orch.reconciling is False     # 对账后解冻


def test_scanner_runs_watchlist():
    venue = PaperVenue()
    orch = _orch(venue=venue)
    scanner = OpportunityScanner(orch, ["BTC/USDT"], interval=0)
    run(scanner.run(max_cycles=1))
    assert orch.metrics.snapshot()["runs"] == 1


def test_monitor_reports_positions():
    venue = PaperVenue()
    orch = _orch(venue=venue)
    run(orch.run_once("BTC/USDT"))             # 先建一个仓
    events = EventBus()
    seen = []
    events.subscribe(lambda e: seen.append(e["type"]))
    report = run(PositionMonitor(venue, events=events).check_once())
    assert len(report["positions"]) == 1
    assert report["alerts"] == []              # paper 原生保护，无告警
    assert "position_monitor" in seen


def test_engine_run_bounded_end_to_end():
    venue = PaperVenue()
    orch = _orch(venue=venue)
    engine = build_engine(Settings(_env_file=None, watchlist="BTC/USDT"), orch, venue)
    run(engine.run_bounded(scan_cycles=1, monitor_cycles=1))
    assert len(run(venue.positions())) == 1    # 对账→扫描成交→监控
    assert orch.metrics.snapshot()["executed"] == 1
