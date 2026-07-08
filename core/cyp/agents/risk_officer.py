"""风控官：在确定性风控引擎之上做 LLM 软评审。

铁律：**只能收紧，不能放宽**。无权批准被硬护栏否决的提案；只能把 approved/downsized
进一步升级为 rejected，或抬高 risk_score。无 LLM 时直接透传引擎结论（llm_reviewed=False）。
"""

from __future__ import annotations

from pydantic import BaseModel, Field

from cyp.agents.base import AgentContext
from cyp.contracts import AnalystReport, RiskAssessment, TradeProposal


class _RiskReview(BaseModel):
    risk_score: float = Field(ge=0.0, le=1.0)
    escalate_reject: bool = False
    notes: str = ""


class RiskOfficer:
    id = "risk_officer"

    async def run(
        self,
        proposal: TradeProposal,
        assessment: RiskAssessment,
        reports: list[AnalystReport],
        ctx: AgentContext,
    ) -> RiskAssessment:
        if assessment.verdict == "rejected":
            return assessment  # 已被硬护栏否决，无需软评审
        if not ctx.llm.enabled:
            return assessment  # 无 LLM：透传，llm_reviewed 保持 False

        drivers = "; ".join(f"{r.agent}:{r.stance}({r.confidence:.2f})" for r in reports)
        review = await ctx.llm.json(
            system=("你是加密交易风控官。只能收紧不能放宽：若发现 thesis 不自洽、极端行情/事件窗口、"
                    "或与已有敞口叠加同向风险，可 escalate_reject=true。给出 0-1 风险分与简短中文说明。"),
            user=f"提案：{proposal.side} {proposal.symbol} 仓位={proposal.size_quote} "
                 f"止损={proposal.stop_loss} 置信={proposal.confidence:.2f}\n分析：{drivers}",
            schema=_RiskReview,
        )
        if review is None:
            return assessment

        verdict = assessment.verdict
        violations = list(assessment.hard_violations)
        if review.escalate_reject:
            verdict = "rejected"
            violations.append(f"risk_officer: {review.notes or '软评审否决'}")
        return assessment.model_copy(update={
            "verdict": verdict,
            "hard_violations": violations,
            "risk_score": max(assessment.risk_score, review.risk_score),  # 取更保守
            "llm_notes": review.notes,
            "llm_reviewed": True,
        })
