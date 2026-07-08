"""编排器端到端（PaperVenue，离线）：完整闭环 / 审批拒 / Kill Switch。"""

import asyncio
from datetime import datetime, timezone
from decimal import Decimal

from cyp.approval import AutoApprove, AutoReject
from cyp.config import Settings
from cyp.contracts import Candle, DerivativesData, MarketSnapshot, SentimentData
from cyp.events import EventBus
from cyp.orchestrator import Orchestrator
from cyp.venue import PaperVenue

run = asyncio.run


class UptrendData:
    """强多头快照：稳定上涨 + 资金费为负（空头拥挤）+ 极端恐惧 → 各维度看多。"""

    async def snapshot(self, symbol: str) -> MarketSnapshot:
        candles = []
        for i in range(80):
            price = Decimal(50000 + i * 125)  # 50000 → ~59875
            candles.append(Candle(ts=datetime.now(timezone.utc), open=price, high=price + 50,
                                  low=price - 50, close=price, volume=Decimal("100")))
        return MarketSnapshot(
            symbol=symbol, venue="synthetic", ohlcv=candles,
            derivatives=DerivativesData(funding_rate=Decimal("-0.0005"), long_short_ratio=Decimal("0.8")),
            sentiment=SentimentData(fear_greed=20),
        )


def _orch(**over):
    settings = over.pop("settings", Settings(_env_file=None))
    return Orchestrator(settings=settings, data_source=over.pop("data", UptrendData()),
                        venue=over.pop("venue", PaperVenue()), **over)


def test_full_loop_executes():
    events = EventBus()
    seen: list[str] = []
    events.subscribe(lambda e: seen.append(e["type"]))
    orch = _orch(events=events, approval=AutoApprove())
    res = run(orch.run_once("BTC/USDT"))

    assert res.status == "executed"
    assert len(res.reports) == 4
    assert res.proposal.side == "long"
    assert res.execution.status == "filled"
    assert res.execution.protective_orders                 # 入场即挂保护单
    assert res.review is not None
    # 事件流覆盖关键节点
    for t in ("run_started", "reports_ready", "proposal_ready", "risk_assessed",
              "approval_decided", "executed", "reviewed"):
        assert t in seen


def test_position_opened_and_lesson_recorded():
    venue = PaperVenue()
    orch = _orch(venue=venue, approval=AutoApprove())
    run(orch.run_once("BTC/USDT"))
    assert len(run(venue.positions())) == 1
    assert orch.memory.get_lessons() is not None


def test_operator_reject_blocks_execution():
    venue = PaperVenue()
    orch = _orch(venue=venue, approval=AutoReject())
    res = run(orch.run_once("BTC/USDT"))
    assert res.status == "not_approved"
    assert len(run(venue.positions())) == 0                 # 未下单


def test_kill_switch_blocks_at_risk_engine():
    settings = Settings(_env_file=None, kill=True)
    orch = _orch(settings=settings, approval=AutoApprove())
    res = run(orch.run_once("BTC/USDT"))
    assert res.status == "rejected"
    assert any("kill_switch" in v for v in res.assessment.hard_violations)


def test_synthetic_run_does_not_crash():
    from cyp.data import SyntheticMarketData
    orch = _orch(data=SyntheticMarketData(), approval=AutoApprove())
    res = run(orch.run_once("BTC/USDT"))
    assert res.status in ("executed", "no_trade", "rejected")
    assert len(res.reports) == 4
