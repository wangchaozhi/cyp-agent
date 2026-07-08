"""审批门接口 + 自动实现（测试/占位）。人工 CLI 实现在 cyp.approval.cli。"""

from __future__ import annotations

from typing import Protocol

from cyp.contracts import ApprovalDecision, RiskAssessment, TradeProposal


class ApprovalGate(Protocol):
    async def decide(self, proposal: TradeProposal, assessment: RiskAssessment) -> ApprovalDecision: ...


class AutoApprove:
    """自动批准（测试 / M6 全自动占位）。生产半自动务必用人工门。"""

    def __init__(self, operator: str = "auto") -> None:
        self._operator = operator

    async def decide(self, proposal: TradeProposal, assessment: RiskAssessment) -> ApprovalDecision:
        return ApprovalDecision(decision="approve", operator=self._operator, note="auto-approve")


class AutoReject:
    def __init__(self, operator: str = "auto") -> None:
        self._operator = operator

    async def decide(self, proposal: TradeProposal, assessment: RiskAssessment) -> ApprovalDecision:
        return ApprovalDecision(decision="reject", operator=self._operator, note="auto-reject")
