"""M1 永续端到端：策略官杠杆决策 + PaperVenue 保证金记账 + 编排器合约闭环。离线。"""

import asyncio
from datetime import datetime, timezone
from decimal import Decimal

from cyp.agents import AgentContext, Strategist
from cyp.approval import AutoApprove
from cyp.config import RiskConfig, Settings
from cyp.contracts import (
    AnalystReport,
    Candle,
    DerivativesData,
    MarketSnapshot,
    OrderIntent,
    SentimentData,
)
from cyp.data import SyntheticMarketData
from cyp.llm import build_llm
from cyp.orchestrator import Orchestrator
from cyp.venue import PaperVenue

run = asyncio.run
CFG = RiskConfig(_env_file=None)


def _ctx(allow_perp: bool) -> AgentContext:
    s = Settings(_env_file=None, allow_perp=allow_perp)
    return AgentContext(llm=build_llm(s), settings=s)


def _bullish():
    return [AnalystReport(agent="technical", stance="bullish", confidence=0.9),
            AnalystReport(agent="derivatives", stance="bullish", confidence=0.8),
            AnalystReport(agent="sentiment", stance="bullish", confidence=0.7)]


def _snap():
    return run(SyntheticMarketData().snapshot("BTC/USDT"))


def test_strategist_proposes_perp_when_allowed():
    p = run(Strategist().run(_bullish(), _snap(), Decimal("10000"), CFG, _ctx(True)))
    assert p.instrument == "perp"
    assert p.leverage > 1.0 and p.leverage <= float(CFG.max_leverage)
    assert p.margin_mode == "isolated"


def test_strategist_stays_spot_when_not_allowed():
    p = run(Strategist().run(_bullish(), _snap(), Decimal("10000"), CFG, _ctx(False)))
    assert p.instrument == "spot" and p.leverage == 1.0


def test_paper_perp_margin_accounting():
    v = PaperVenue(initial_quote=Decimal("10000"))
    v.set_mark_price("BTC/USDT", Decimal("100"))
    # 名义 1000、5x → 只扣保证金 200（+手续费），远小于现货全额
    intent = OrderIntent(client_id="p1", symbol="BTC/USDT", venue="paper", side="long",
                         instrument="perp", size_quote=Decimal("1000"), leverage=5.0,
                         stop_loss=Decimal("90"))
    res = run(v.place(intent))
    assert res.status == "filled"
    free = run(v.balances()).free_quote
    assert Decimal("9799") < free < Decimal("9801")     # 10000 - 200 - 手续费≈0.4
    pos = run(v.positions())[0]
    assert pos.instrument == "perp" and pos.leverage == 5.0


def test_orchestrator_perp_end_to_end():
    class UptrendData:
        async def snapshot(self, symbol):
            candles = [Candle(ts=datetime.now(timezone.utc), open=Decimal(50000 + i * 125),
                              high=Decimal(50000 + i * 125 + 50), low=Decimal(50000 + i * 125 - 50),
                              close=Decimal(50000 + i * 125), volume=Decimal("100")) for i in range(80)]
            return MarketSnapshot(symbol=symbol, venue="synthetic", ohlcv=candles,
                                  derivatives=DerivativesData(funding_rate=Decimal("-0.0005"),
                                                              long_short_ratio=Decimal("0.8")),
                                  sentiment=SentimentData(fear_greed=20))

    settings = Settings(_env_file=None, allow_perp=True)
    venue = PaperVenue()
    orch = Orchestrator(settings, UptrendData(), venue, approval=AutoApprove())
    res = run(orch.run_once("BTC/USDT"))
    assert res.status == "executed"
    pos = run(venue.positions())[0]
    assert pos.instrument == "perp" and pos.leverage > 1.0    # 走了合约且带杠杆
