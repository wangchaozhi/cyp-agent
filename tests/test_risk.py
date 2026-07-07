"""风控引擎单测：每条硬护栏的通过/否决/缩仓边界。

基线场景：账户净值 10000 USDT，参考价 60000，做多 1000 名义、止损 58000。
    单笔风险 = 1000 × (2000/60000) ≈ 33.3 < 预算 100 → 默认应 approved。
"""

from decimal import Decimal

import pytest

from cyp.config import RiskConfig
from cyp.contracts import TradeProposal
from cyp.risk import assess
from cyp.risk.rules import RiskContext

CFG = RiskConfig(_env_file=None)


def prop(**over) -> TradeProposal:
    base = dict(
        symbol="BTC/USDT", venue="binance", side="long", instrument="spot",
        size_quote=Decimal("1000"), leverage=1.0, stop_loss=Decimal("58000"), confidence=0.7,
    )
    base.update(over)
    return TradeProposal(**base)


def ctx(**over) -> RiskContext:
    base = dict(equity_quote=Decimal("10000"), ref_price=Decimal("60000"))
    base.update(over)
    return RiskContext(**base)


# ---- 通过基线 --------------------------------------------------------------

def test_approved_baseline():
    r = assess(prop(), ctx(), CFG)
    assert r.verdict == "approved"
    assert r.hard_violations == []
    assert 0.0 <= r.risk_score <= 1.0


# ---- 止损相关 --------------------------------------------------------------

def test_missing_stop_loss_rejected():
    r = assess(prop(stop_loss=None), ctx(), CFG)
    assert r.verdict == "rejected"
    assert any("stop_loss_required" in v for v in r.hard_violations)


def test_long_stop_above_price_rejected():
    r = assess(prop(stop_loss=Decimal("61000")), ctx(), CFG)
    assert r.verdict == "rejected"
    assert any("stop_loss_required" in v for v in r.hard_violations)


def test_short_stop_below_price_rejected():
    r = assess(prop(side="short", stop_loss=Decimal("59000")), ctx(), CFG)
    assert r.verdict == "rejected"


# ---- Kill Switch / 对账（含"平仓放行"）------------------------------------

def test_kill_switch_blocks_open():
    r = assess(prop(), ctx(kill=True), CFG)
    assert r.verdict == "rejected"
    assert any("kill_switch" in v for v in r.hard_violations)


def test_kill_switch_allows_close():
    # 平仓（side=flat）不应被 Kill Switch 阻断——必须能退出
    r = assess(prop(side="flat", size_quote=Decimal("0"), stop_loss=None), ctx(kill=True), CFG)
    assert r.verdict == "approved"


def test_reconciling_blocks_open():
    r = assess(prop(), ctx(reconciling=True), CFG)
    assert r.verdict == "rejected"
    assert any("reconciling" in v for v in r.hard_violations)


def test_reconciling_allows_close():
    r = assess(prop(side="flat", size_quote=Decimal("0"), stop_loss=None), ctx(reconciling=True), CFG)
    assert r.verdict == "approved"


# ---- 杠杆 / 爆仓缓冲 -------------------------------------------------------

def test_leverage_over_limit_rejected():
    r = assess(prop(instrument="perp", leverage=5.0), ctx(), CFG)
    assert r.verdict == "rejected"
    assert any("leverage" in v for v in r.hard_violations)


def test_liq_buffer_too_small_rejected():
    # 爆仓价 59000，缓冲 = 1000/60000 ≈ 0.017 < 0.30
    r = assess(prop(instrument="perp", leverage=3.0), ctx(est_liq_price=Decimal("59000")), CFG)
    assert r.verdict == "rejected"
    assert any("liq_buffer" in v for v in r.hard_violations)


def test_liq_buffer_ok_when_far():
    r = assess(prop(instrument="perp", leverage=3.0), ctx(est_liq_price=Decimal("40000")), CFG)
    assert r.verdict == "approved"


# ---- 滑点 / 价格冲击 ------------------------------------------------------

def test_slippage_over_limit_rejected():
    r = assess(prop(), ctx(est_slippage_bps=Decimal("50")), CFG)
    assert r.verdict == "rejected"
    assert any("slippage" in v for v in r.hard_violations)


def test_price_impact_over_limit_rejected():
    r = assess(prop(), ctx(est_price_impact=Decimal("0.02")), CFG)
    assert r.verdict == "rejected"
    assert any("price_impact" in v for v in r.hard_violations)


# ---- 频率 / 连亏 / 回撤熔断 -----------------------------------------------

def test_order_rate_rejected():
    r = assess(prop(), ctx(orders_last_hour=10), CFG)
    assert r.verdict == "rejected"


def test_consecutive_losses_rejected():
    r = assess(prop(), ctx(consecutive_losses=4), CFG)
    assert r.verdict == "rejected"


@pytest.mark.parametrize("field,value", [
    ("daily_drawdown", Decimal("0.03")),
    ("weekly_drawdown", Decimal("0.08")),
    ("total_drawdown", Decimal("0.15")),
])
def test_drawdown_circuit_rejected(field, value):
    r = assess(prop(), ctx(**{field: value}), CFG)
    assert r.verdict == "rejected"
    assert any("drawdown_circuit" in v for v in r.hard_violations)


# ---- 缩仓路径 --------------------------------------------------------------

def test_per_trade_risk_downsize():
    # 止损 54000（距离 10%），size 2000 → 风险 200 > 预算 100 → 缩到 1000
    r = assess(prop(stop_loss=Decimal("54000"), size_quote=Decimal("2000")), ctx(), CFG)
    assert r.verdict == "downsized"
    assert r.adjusted_size_quote == Decimal("1000")


def test_position_cap_downsize():
    # 止损很近（不触发单笔风险），size 3000 > 单仓上限 2000 → 缩到 2000
    r = assess(prop(stop_loss=Decimal("59700"), size_quote=Decimal("3000")), ctx(), CFG)
    assert r.verdict == "downsized"
    assert r.adjusted_size_quote == Decimal("2000")


def test_gross_exposure_downsize_to_room():
    r = assess(prop(stop_loss=Decimal("59700"), size_quote=Decimal("1000")),
               ctx(gross_exposure_quote=Decimal("9500")), CFG)
    assert r.verdict == "downsized"
    assert r.adjusted_size_quote == Decimal("500")  # 剩余空间


def test_gross_exposure_full_rejected():
    r = assess(prop(stop_loss=Decimal("59700"), size_quote=Decimal("1000")),
               ctx(gross_exposure_quote=Decimal("10000")), CFG)
    assert r.verdict == "rejected"
    assert any("gross_exposure" in v for v in r.hard_violations)


def test_symbol_concentration_downsize():
    r = assess(prop(stop_loss=Decimal("59700"), size_quote=Decimal("1000")),
               ctx(symbol_exposure_quote=Decimal("2800")), CFG)
    assert r.verdict == "downsized"
    assert r.adjusted_size_quote == Decimal("200")


def test_downsize_picks_minimum_cap():
    # 止损 54000（距离 10%），size 5000：
    #   per_trade 缩到 100/0.10 = 1000；position_cap 缩到 2000 → 取最小 1000
    r = assess(prop(stop_loss=Decimal("54000"), size_quote=Decimal("5000")), ctx(), CFG)
    assert r.verdict == "downsized"
    assert r.adjusted_size_quote == Decimal("1000")


# ---- 否决优先于缩仓 --------------------------------------------------------

def test_reject_takes_precedence_over_downsize():
    # 同时触发缩仓（超单仓）与否决（超杠杆）→ 最终 rejected
    r = assess(prop(instrument="perp", leverage=9.0, size_quote=Decimal("5000")), ctx(), CFG)
    assert r.verdict == "rejected"
    assert any("leverage" in v for v in r.hard_violations)
