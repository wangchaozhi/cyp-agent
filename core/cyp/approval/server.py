"""PendingApprovalGate：把审批挂起为一个 Future，由外部（HTTP 端点/仪表盘按钮）解决。

这是 Web 半自动的核心：编排器走到审批门时 decide() 挂起并发 awaiting_approval 事件，
仪表盘展示待审批卡片，操作员点按钮 → resolve() 设置结果 → 编排器继续。
超时=拒绝（fail-safe）。
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from decimal import Decimal, InvalidOperation

from cyp.contracts import ApprovalDecision, RiskAssessment, TradeProposal
from cyp.events import EventBus


@dataclass
class _Pending:
    future: asyncio.Future
    proposal: TradeProposal
    assessment: RiskAssessment


class PendingApprovalGate:
    def __init__(self, timeout: float = 1800, events: EventBus | None = None,
                 operator: str = "dashboard") -> None:
        self._timeout = timeout
        self._events = events
        self._operator = operator
        self._pending: dict[str, _Pending] = {}

    async def decide(self, proposal: TradeProposal, assessment: RiskAssessment,
                     run_id: str = "") -> ApprovalDecision:
        loop = asyncio.get_event_loop()
        fut: asyncio.Future = loop.create_future()
        self._pending[run_id] = _Pending(fut, proposal, assessment)
        if self._events:
            await self._events.publish("awaiting_approval", run_id, symbol=proposal.symbol,
                                       proposal=proposal.model_dump(mode="json"),
                                       assessment=assessment.model_dump(mode="json"))
        try:
            return await asyncio.wait_for(fut, timeout=self._timeout)
        except asyncio.TimeoutError:
            return ApprovalDecision(decision="reject", operator=self._operator, note="审批超时(fail-safe)")
        finally:
            self._pending.pop(run_id, None)

    def resolve(self, run_id: str, decision: str, size: str | float | None = None,
                note: str = "", operator: str | None = None) -> bool:
        """由 HTTP 端点调用。返回是否成功解决一个待审批项。operator 记入审计（多操作员）。"""
        item = self._pending.get(run_id)
        if item is None or item.future.done():
            return False
        who = (operator or "").strip() or self._operator
        if decision == "modify" and size is not None:
            try:
                modified = item.proposal.model_copy(update={"size_quote": Decimal(str(size))})
                dec = ApprovalDecision(decision="modify", modified=modified,
                                       operator=who, note=note or f"改规模至 {size}")
            except (InvalidOperation, ValueError):
                return False
        elif decision == "approve":
            dec = ApprovalDecision(decision="approve", operator=who, note=note or "仪表盘批准")
        else:
            dec = ApprovalDecision(decision="reject", operator=who, note=note or "仪表盘拒绝")
        item.future.set_result(dec)
        return True

    def list_pending(self) -> list[dict]:
        return [{"run_id": rid, "proposal": p.proposal.model_dump(mode="json"),
                 "assessment": p.assessment.model_dump(mode="json")}
                for rid, p in self._pending.items()]
