"""审批门接口 + 自动实现（测试/占位）+ 策略化自动审批（M6）。人工 CLI 实现在 cyp.approval.cli。"""

from __future__ import annotations

from decimal import Decimal
from typing import Protocol

from cyp.contracts import ApprovalDecision, RiskAssessment, TradeProposal


class ApprovalGate(Protocol):
    async def decide(self, proposal: TradeProposal, assessment: RiskAssessment,
                     run_id: str = "") -> ApprovalDecision: ...


class AutoApprove:
    """自动批准（测试 / M6 全自动占位）。生产半自动务必用人工门。"""

    def __init__(self, operator: str = "auto") -> None:
        self._operator = operator

    async def decide(self, proposal: TradeProposal, assessment: RiskAssessment,
                     run_id: str = "") -> ApprovalDecision:
        return ApprovalDecision(decision="approve", operator=self._operator, note="auto-approve")


class AutoReject:
    def __init__(self, operator: str = "auto") -> None:
        self._operator = operator

    async def decide(self, proposal: TradeProposal, assessment: RiskAssessment,
                     run_id: str = "") -> ApprovalDecision:
        return ApprovalDecision(decision="reject", operator=self._operator, note="auto-reject")


class PolicyApprovalGate:
    """M6 策略化自动审批：**同时满足**「标的白名单 + risk_score < 阈值 + 金额 < 上限」
    才自动批准，任一不满足即转交内层人工门。只放行不收权：硬护栏与 Kill Switch 照常生效。"""

    def __init__(self, fallback: ApprovalGate, symbols: list[str] | None = None,
                 max_risk_score: float = 0.5, max_quote: Decimal = Decimal("200"),
                 operator: str = "policy-auto") -> None:
        self._fallback = fallback
        self._symbols = set(symbols or [])
        self._max_risk_score = max_risk_score
        self._max_quote = max_quote
        self._operator = operator

    def _policy_misses(self, proposal: TradeProposal, assessment: RiskAssessment) -> list[str]:
        misses: list[str] = []
        if proposal.symbol not in self._symbols:
            misses.append(f"{proposal.symbol} 不在白名单")
        if assessment.risk_score >= self._max_risk_score:
            misses.append(f"risk_score {assessment.risk_score:.2f} ≥ {self._max_risk_score}")
        size = assessment.adjusted_size_quote if assessment.adjusted_size_quote is not None \
            else proposal.size_quote
        if size > self._max_quote:
            misses.append(f"金额 {size} > 上限 {self._max_quote}")
        return misses

    async def decide(self, proposal: TradeProposal, assessment: RiskAssessment,
                     run_id: str = "") -> ApprovalDecision:
        misses = self._policy_misses(proposal, assessment)
        if not misses:
            return ApprovalDecision(decision="approve", operator=self._operator,
                                    note="策略自动批准：白名单+低风险+小额")
        return await self._fallback.decide(proposal, assessment, run_id)
