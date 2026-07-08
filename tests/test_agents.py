"""多智能体：分析师降级/方向、策略官仓位反推、风控官透传、交易员执行、复盘。离线确定性。"""

import asyncio
from decimal import Decimal

from cyp.agents import (
    AgentContext,
    DerivativesAnalyst,
    OnchainAnalyst,
    Reviewer,
    RiskOfficer,
    SentimentAnalyst,
    Strategist,
    TechnicalAnalyst,
    Trader,
)
from cyp.config import RiskConfig, Settings
from cyp.contracts import (
    AnalystReport,
    ExecutionResult,
    MarketSnapshot,
    RiskAssessment,
    TradeProposal,
)
from cyp.data import SyntheticMarketData
from cyp.llm import build_llm
from cyp.venue import PaperVenue

run = asyncio.run
CFG = RiskConfig(_env_file=None)


def ctx() -> AgentContext:
    settings = Settings(_env_file=None)
    return AgentContext(llm=build_llm(settings), settings=settings)


def snap() -> MarketSnapshot:
    return run(SyntheticMarketData().snapshot("BTC/USDT"))


# ---- 分析师 ----------------------------------------------------------------

def test_technical_not_degraded_on_data():
    r = run(TechnicalAnalyst().run(snap(), ctx()))
    assert r.agent == "technical" and not r.degraded and r.signals


def test_derivatives_and_sentiment_present():
    s = snap()
    assert not run(DerivativesAnalyst().run(s, ctx())).degraded
    assert not run(SentimentAnalyst().run(s, ctx())).degraded


def test_onchain_degrades_without_data():
    r = run(OnchainAnalyst().run(snap(), ctx()))   # 合成源无链上数据
    assert r.degraded and r.stance == "neutral"


def test_technical_degrades_without_candles():
    empty = MarketSnapshot(symbol="BTC/USDT", venue="x")
    r = run(TechnicalAnalyst().run(empty, ctx()))
    assert r.degraded


# ---- 策略官 ----------------------------------------------------------------

def _bullish_reports():
    return [
        AnalystReport(agent="technical", stance="bullish", confidence=0.8),
        AnalystReport(agent="derivatives", stance="bullish", confidence=0.7),
        AnalystReport(agent="sentiment", stance="bullish", confidence=0.6),
        AnalystReport(agent="onchain", stance="neutral", confidence=0.2, degraded=True),
    ]


def test_strategist_goes_long_and_sizes_by_risk_budget():
    s = snap()
    equity = Decimal("10000")
    prop = run(Strategist().run(_bullish_reports(), s, equity, CFG, ctx()))
    assert prop.side == "long"
    assert prop.size_quote > 0
    assert prop.stop_loss is not None and prop.stop_loss < Decimal(str(s.ohlcv[-1].close))
    # 单笔风险 = size × 止损距离占比 应 ≈ 账户 × 1% = 100（允许量化误差）
    ref = Decimal(str(s.ohlcv[-1].close))
    stop_frac = abs(ref - prop.stop_loss) / ref
    risk = prop.size_quote * stop_frac
    assert abs(risk - Decimal("100")) < Decimal("2")


def test_strategist_flat_on_weak_signal():
    weak = [AnalystReport(agent="technical", stance="neutral", confidence=0.1)]
    prop = run(Strategist().run(weak, snap(), Decimal("10000"), CFG, ctx()))
    assert prop.side == "flat" and prop.size_quote == 0


# ---- 风控官 ----------------------------------------------------------------

def test_risk_officer_passthrough_without_llm():
    assessment = RiskAssessment(verdict="approved", risk_score=0.3)
    prop = TradeProposal(symbol="BTC/USDT", venue="paper", side="long",
                         size_quote=Decimal("1000"), stop_loss=Decimal("58000"), confidence=0.6)
    out = run(RiskOfficer().run(prop, assessment, [], ctx()))
    assert out.verdict == "approved" and out.llm_reviewed is False


def test_risk_officer_never_revives_rejected():
    rejected = RiskAssessment(verdict="rejected", hard_violations=["x"], risk_score=1.0)
    prop = TradeProposal(symbol="BTC/USDT", venue="paper", side="long",
                         size_quote=Decimal("1000"), stop_loss=Decimal("58000"), confidence=0.6)
    out = run(RiskOfficer().run(prop, rejected, [], ctx()))
    assert out.verdict == "rejected"


# ---- 交易员 + 复盘官 -------------------------------------------------------

def test_trader_executes_with_protective_orders():
    v = PaperVenue()
    v.set_mark_price("BTC/USDT", Decimal("60000"))
    prop = TradeProposal(symbol="BTC/USDT", venue="paper", side="long", size_quote=Decimal("1000"),
                         stop_loss=Decimal("58000"), take_profit=[Decimal("64000")], confidence=0.6)
    res = run(Trader().execute(prop, v, client_id="t1"))
    assert res.status == "filled"
    assert {p.kind for p in res.protective_orders} == {"stop_loss", "take_profit"}


def test_reviewer_scores_execution():
    prop = TradeProposal(symbol="BTC/USDT", venue="paper", side="long", size_quote=Decimal("1000"),
                         stop_loss=Decimal("58000"), confidence=0.6)
    res = ExecutionResult(client_id="t1", status="filled", filled_base=Decimal("0.016"),
                          avg_price=Decimal("60030"), slippage_bps=Decimal("5"))
    review = run(Reviewer().run(prop, res, ctx()))
    assert review.score > 0 and review.symbol == "BTC/USDT"
