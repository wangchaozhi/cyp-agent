"""M6 策略化自动审批：白名单+risk_score+金额三重条件，任一不满足转人工。离线确定性。"""

import asyncio
from decimal import Decimal

from cyp.approval import AutoReject, PolicyApprovalGate, wrap_with_policy
from cyp.config import Settings
from cyp.contracts import RiskAssessment, TradeProposal

run = asyncio.run


def _prop(symbol="BTC/USDT", size="100") -> TradeProposal:
    return TradeProposal(symbol=symbol, venue="paper", side="long",
                         size_quote=Decimal(size), stop_loss=Decimal("58000"), confidence=0.6)


def _assess(risk_score=0.2, adjusted=None) -> RiskAssessment:
    return RiskAssessment(verdict="approved", risk_score=risk_score,
                          adjusted_size_quote=Decimal(adjusted) if adjusted else None)


def _gate(**kw) -> PolicyApprovalGate:
    defaults = {"fallback": AutoReject(), "symbols": ["BTC/USDT"],
                "max_risk_score": 0.5, "max_quote": Decimal("200")}
    defaults.update(kw)
    return PolicyApprovalGate(**defaults)


def test_auto_approves_whitelisted_small_low_risk():
    d = run(_gate().decide(_prop(), _assess()))
    assert d.decision == "approve" and d.operator == "policy-auto"


def test_falls_back_when_not_whitelisted():
    d = run(_gate().decide(_prop(symbol="DOGE/USDT"), _assess()))
    assert d.decision == "reject"          # fallback = AutoReject，证明走了人工路径


def test_falls_back_on_high_risk_score():
    d = run(_gate().decide(_prop(), _assess(risk_score=0.7)))
    assert d.decision == "reject"


def test_falls_back_on_large_size():
    d = run(_gate().decide(_prop(size="500"), _assess()))
    assert d.decision == "reject"


def test_uses_downsized_amount_for_cap():
    # 原始 500 超上限，但风控已降档到 150 → 按降档后金额判定，可自动批准
    d = run(_gate().decide(_prop(size="500"), _assess(adjusted="150")))
    assert d.decision == "approve"


def test_wrap_with_policy_honors_settings():
    s = Settings(_env_file=None, approval="auto", auto_symbols="BTC/USDT")
    gate = wrap_with_policy(s, AutoReject())
    assert isinstance(gate, PolicyApprovalGate)

    s2 = Settings(_env_file=None, approval="cli")
    inner = AutoReject()
    assert wrap_with_policy(s2, inner) is inner


def test_empty_whitelist_never_auto_approves():
    gate = _gate(symbols=[])
    d = run(gate.decide(_prop(), _assess()))
    assert d.decision == "reject"
