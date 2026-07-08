"""PendingApprovalGate：挂起-解决 / 超时 / 修改 + 与编排器的 Web 审批闭环。离线。"""

import asyncio
from datetime import datetime, timezone
from decimal import Decimal

from cyp.approval import PendingApprovalGate
from cyp.config import Settings
from cyp.contracts import Candle, DerivativesData, MarketSnapshot, RiskAssessment, SentimentData, TradeProposal
from cyp.events import EventBus
from cyp.orchestrator import Orchestrator
from cyp.venue import PaperVenue

run = asyncio.run


def _prop():
    return TradeProposal(symbol="BTC/USDT", venue="paper", side="long",
                         size_quote=Decimal("1000"), stop_loss=Decimal("58000"), confidence=0.6)


class UptrendData:
    async def snapshot(self, symbol: str) -> MarketSnapshot:
        candles = [Candle(ts=datetime.now(timezone.utc), open=Decimal(50000 + i * 125),
                          high=Decimal(50000 + i * 125 + 50), low=Decimal(50000 + i * 125 - 50),
                          close=Decimal(50000 + i * 125), volume=Decimal("100")) for i in range(80)]
        return MarketSnapshot(symbol=symbol, venue="synthetic", ohlcv=candles,
                              derivatives=DerivativesData(funding_rate=Decimal("-0.0005"),
                                                          long_short_ratio=Decimal("0.8")),
                              sentiment=SentimentData(fear_greed=20))


async def _resolve_when_pending(gate: PendingApprovalGate, run_id: str, decision: str, size=None):
    for _ in range(2000):
        if any(p["run_id"] == run_id for p in gate.list_pending()):
            return gate.resolve(run_id, decision, size=size)
        await asyncio.sleep(0.001)
    return False


def test_resolve_approve():
    async def scenario():
        gate = PendingApprovalGate(timeout=5)
        task = asyncio.create_task(gate.decide(_prop(), RiskAssessment(verdict="approved"), run_id="r1"))
        assert await _resolve_when_pending(gate, "r1", "approve")
        return await task
    assert run(scenario()).decision == "approve"


def test_resolve_modify_size():
    async def scenario():
        gate = PendingApprovalGate(timeout=5)
        task = asyncio.create_task(gate.decide(_prop(), RiskAssessment(verdict="approved"), run_id="r2"))
        await _resolve_when_pending(gate, "r2", "modify", size="500")
        return await task
    d = run(scenario())
    assert d.decision == "modify" and d.modified.size_quote == Decimal("500")


def test_timeout_rejects():
    gate = PendingApprovalGate(timeout=0.01)
    d = run(gate.decide(_prop(), RiskAssessment(verdict="approved"), run_id="r3"))
    assert d.decision == "reject"


def test_orchestrator_web_approval_loop():
    async def scenario():
        events = EventBus()
        gate = PendingApprovalGate(timeout=5, events=events)
        venue = PaperVenue()
        orch = Orchestrator(Settings(_env_file=None), UptrendData(), venue, events=events, approval=gate)
        task = asyncio.create_task(orch.run_once("BTC/USDT", run_id="runW"))
        assert await _resolve_when_pending(gate, "runW", "approve")
        res = await task
        assert res.status == "executed"
        assert len(await venue.positions()) == 1
    run(scenario())
