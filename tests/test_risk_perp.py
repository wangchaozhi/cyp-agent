"""M1 合约专项护栏：杠杆 / 爆仓缓冲 / 逐仓强制 / 维持保证金。"""

from decimal import Decimal

from cyp.config import RiskConfig
from cyp.contracts import TradeProposal
from cyp.risk import assess
from cyp.risk.rules import RiskContext

CFG = RiskConfig(_env_file=None)


def perp(**over) -> TradeProposal:
    base = {"symbol": "BTC/USDT", "venue": "paper", "side": "long", "instrument": "perp",
                "size_quote": Decimal("1000"), "leverage": 3.0, "margin_mode": "isolated",
                "stop_loss": Decimal("58000"), "confidence": 0.7}
    base.update(over)
    return TradeProposal(**base)


def ctx(**over) -> RiskContext:
    base = {"equity_quote": Decimal("10000"), "ref_price": Decimal("60000")}
    base.update(over)
    return RiskContext(**base)


def test_perp_within_limits_approved():
    r = assess(perp(), ctx(est_liq_price=Decimal("40000")), CFG)
    assert r.verdict in ("approved", "downsized")


def test_cross_margin_rejected_when_forced_isolated():
    r = assess(perp(margin_mode="cross"), ctx(est_liq_price=Decimal("40000")), CFG)
    assert r.verdict == "rejected"
    assert any("margin_mode" in v for v in r.hard_violations)


def test_low_margin_ratio_freezes_open():
    r = assess(perp(), ctx(est_liq_price=Decimal("40000"), margin_ratio=Decimal("0.03")), CFG)
    assert r.verdict == "rejected"
    assert any("maintenance_margin" in v for v in r.hard_violations)


def test_leverage_over_limit_rejected():
    r = assess(perp(leverage=8.0), ctx(est_liq_price=Decimal("40000")), CFG)
    assert r.verdict == "rejected"
    assert any("leverage" in v for v in r.hard_violations)


def test_liq_buffer_too_close_rejected():
    r = assess(perp(), ctx(est_liq_price=Decimal("59000")), CFG)   # 缓冲 ~1.7% < 30%
    assert r.verdict == "rejected"
    assert any("liq_buffer" in v for v in r.hard_violations)


def test_spot_ignores_perp_rules():
    # 现货即便 margin_mode=cross 也不触发合约护栏
    r = assess(perp(instrument="spot", leverage=1.0, margin_mode="cross"),
               ctx(margin_ratio=Decimal("0.01")), CFG)
    assert r.verdict in ("approved", "downsized")
