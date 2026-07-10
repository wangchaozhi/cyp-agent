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
    # 放宽单仓上限，让风险预算成为约束项，验证「size = 预算 / 止损距离」
    cfg = RiskConfig(_env_file=None, max_position_pct=Decimal("1.0"))
    prop = run(Strategist().run(_bullish_reports(), s, equity, cfg, ctx()))
    assert prop.side == "long"
    assert prop.size_quote > 0
    assert prop.stop_loss is not None and prop.stop_loss < Decimal(str(s.ohlcv[-1].close))
    # 单笔风险 = size × 止损距离占比 应 ≈ 账户 × 1% = 100（含 0.995 安全边际与量化误差）
    ref = Decimal(str(s.ohlcv[-1].close))
    stop_frac = abs(ref - prop.stop_loss) / ref
    risk = prop.size_quote * stop_frac
    assert abs(risk - Decimal("100")) < Decimal("2")
    assert risk <= Decimal("100")            # 永不超预算（主动贴合护栏）


def test_strategist_clamps_size_to_position_cap():
    s = snap()
    equity = Decimal("10000")
    prop = run(Strategist().run(_bullish_reports(), s, equity, CFG, ctx()))
    cap = equity * CFG.max_position_pct     # 默认 20% → 2000
    assert prop.side == "long"
    assert prop.size_quote <= cap           # 止损近 → 预算反推超上限 → 夹到单仓上限


def test_strategist_flat_on_weak_signal():
    weak = [AnalystReport(agent="technical", stance="neutral", confidence=0.1)]
    prop = run(Strategist().run(weak, snap(), Decimal("10000"), CFG, ctx()))
    assert prop.side == "flat" and prop.size_quote == 0


def _bearish_reports():
    return [
        AnalystReport(agent="technical", stance="bearish", confidence=0.8),
        AnalystReport(agent="derivatives", stance="bearish", confidence=0.7),
    ]


def _perp_snap() -> MarketSnapshot:
    return run(SyntheticMarketData().snapshot("BTC/USDT:USDT"))


def test_strategist_uses_perp_for_perp_symbol_even_at_low_confidence():
    # 永续符号 + 低置信：工具必须是 perp（现货参数下单必被交易所拒），杠杆保持 1x
    weak_bear = [AnalystReport(agent="technical", stance="bearish", confidence=0.2)]
    settings = Settings(_env_file=None, allow_perp=True)
    c = AgentContext(llm=build_llm(settings), settings=settings)
    prop = run(Strategist().run(weak_bear, _perp_snap(), Decimal("10000"), CFG, c))
    assert prop.side == "short"
    assert prop.instrument == "perp"
    assert prop.leverage == 1.0


def test_strategist_flat_on_perp_symbol_when_perp_disallowed():
    settings = Settings(_env_file=None, allow_perp=False)
    c = AgentContext(llm=build_llm(settings), settings=settings)
    prop = run(Strategist().run(_bearish_reports(), _perp_snap(), Decimal("10000"), CFG, c))
    assert prop.side == "flat"
    assert "allow_perp" in prop.thesis


def test_strategist_flat_on_short_spot():
    # 现货符号 + 看空 + 不允许合约：无法裸卖空 → 观望
    prop = run(Strategist().run(_bearish_reports(), snap(), Decimal("10000"), CFG, ctx()))
    assert prop.side == "flat"
    assert "卖空" in prop.thesis


def test_strategist_avoids_adding_to_same_direction_position():
    from cyp.contracts import Position
    s = snap()
    held = [Position(symbol=s.symbol, venue="paper", side="long",
                     size_base=Decimal("0.1"), entry_price=Decimal("60000"))]
    prop = run(Strategist().run(_bullish_reports(), s, Decimal("10000"), CFG, ctx(), positions=held))
    assert prop.side == "flat"
    assert "同向" in prop.thesis


def test_strategist_downsizes_near_cluster_limit():
    from cyp.contracts import Position
    s = snap()
    equity = Decimal("10000")
    # major 簇同向敞口 = 4500，上限 = 10000×0.5 = 5000 → 已达 90%，剩余额度 500
    held = [Position(symbol="ETH/USDT", venue="paper", side="long",
                     size_base=Decimal("1"), entry_price=Decimal("4500"))]
    prop = run(Strategist().run(_bullish_reports(), s, equity, CFG, ctx(), positions=held))
    assert prop.side == "long"
    assert prop.size_quote <= Decimal("500")


def test_strategist_flat_when_cluster_limit_exhausted():
    from cyp.contracts import Position
    s = snap()
    held = [Position(symbol="ETH/USDT", venue="paper", side="long",
                     size_base=Decimal("1"), entry_price=Decimal("6000"))]
    prop = run(Strategist().run(_bullish_reports(), s, Decimal("10000"), CFG, ctx(), positions=held))
    assert prop.side == "flat"
    assert "上限" in prop.thesis


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
