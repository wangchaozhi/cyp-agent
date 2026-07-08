"""CLIApprovalGate：决策解析 + 超时 fail-safe。离线。"""

import asyncio
import time
from decimal import Decimal

from cyp.approval.cli import CLIApprovalGate
from cyp.contracts import RiskAssessment, TradeProposal

run = asyncio.run
def SILENT(*_):
    return None


def _prop():
    return TradeProposal(symbol="BTC/USDT", venue="paper", side="long",
                         size_quote=Decimal("1000"), stop_loss=Decimal("58000"), confidence=0.6)


def _assess():
    return RiskAssessment(verdict="approved", risk_score=0.3)


def gate(answer, timeout=5):
    return CLIApprovalGate(timeout=timeout, input_fn=lambda prompt: answer, print_fn=SILENT)


def test_approve():
    d = run(gate("approve").decide(_prop(), _assess()))
    assert d.decision == "approve"


def test_reject():
    d = run(gate("reject").decide(_prop(), _assess()))
    assert d.decision == "reject"


def test_modify_with_size():
    d = run(gate("modify 500").decide(_prop(), _assess()))
    assert d.decision == "modify" and d.modified.size_quote == Decimal("500")


def test_modify_without_size_falls_back_to_reject():
    d = run(gate("modify").decide(_prop(), _assess()))
    assert d.decision == "reject"


def test_timeout_is_failsafe_reject():
    slow = CLIApprovalGate(timeout=0.01, print_fn=SILENT,
                           input_fn=lambda prompt: (time.sleep(0.2), "approve")[1])
    d = run(slow.decide(_prop(), _assess()))
    assert d.decision == "reject"        # 超时=拒绝（绝不默认放行）
