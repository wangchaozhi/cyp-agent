"""编排器：串联 7 步闭环，逐步发事件 + 落检查点，驱动风控反馈重议。

采集 → 分析(并行,失败隔离) → 决策 → 风控引擎(硬护栏) → 风控官(软评审)
     → [审批门] → 交易员(幂等执行,入场即挂保护单) → 复盘官(经验回灌)

依赖全部注入（data/venue/agents/llm/events/memory/approval/settings），不读全局单例。
"""

from __future__ import annotations

import asyncio
import uuid
from decimal import Decimal
from typing import Literal

from pydantic import BaseModel

from cyp.agents import ANALYSTS, AgentContext, Reviewer, RiskOfficer, Strategist, Trader
from cyp.approval import ApprovalGate, AutoApprove
from cyp.config import Settings
from cyp.contracts import (
    AnalystReport,
    ApprovalDecision,
    ExecutionResult,
    OrderIntent,
    RiskAssessment,
    TradeProposal,
    TradeReview,
)
from cyp.data import MarketDataSource
from cyp.events import EventBus
from cyp.llm import ResilientLLM, build_llm
from cyp.memory import MemoryStore
from cyp.risk import assess as risk_assess
from cyp.risk.rules import RiskContext

RunStatus = Literal["no_trade", "rejected", "not_approved", "executed", "error"]


class RunResult(BaseModel):
    run_id: str
    symbol: str
    status: RunStatus
    reports: list[AnalystReport] = []
    proposal: TradeProposal | None = None
    assessment: RiskAssessment | None = None
    decision: ApprovalDecision | None = None
    execution: ExecutionResult | None = None
    review: TradeReview | None = None
    error: str | None = None


class Orchestrator:
    def __init__(
        self,
        settings: Settings,
        data_source: MarketDataSource,
        venue,
        events: EventBus | None = None,
        memory: MemoryStore | None = None,
        approval: ApprovalGate | None = None,
        llm: ResilientLLM | None = None,
    ) -> None:
        self.settings = settings
        self.data = data_source
        self.venue = venue
        self.events = events or EventBus()
        self.memory = memory or MemoryStore()
        self.approval = approval or AutoApprove()
        self.llm = llm or build_llm(settings)
        self.strategist = Strategist()
        self.risk_officer = RiskOfficer()
        self.trader = Trader()
        self.reviewer = Reviewer()

    async def run_once(self, symbol: str, run_id: str | None = None) -> RunResult:
        run_id = run_id or uuid.uuid4().hex[:12]
        ctx = AgentContext(llm=self.llm, settings=self.settings, lessons=self.memory.get_lessons())
        try:
            return await self._run(symbol, run_id, ctx)
        except Exception as e:  # noqa: BLE001 —— 单轮失败隔离，不击穿调用方
            await self.events.publish("run_failed", run_id, symbol=symbol, error=str(e))
            return RunResult(run_id=run_id, symbol=symbol, status="error", error=str(e))

    async def _run(self, symbol: str, run_id: str, ctx: AgentContext) -> RunResult:
        cfg = self.settings.risk
        await self.events.publish("run_started", run_id, symbol=symbol)

        # ① 采集
        snap = await self.data.snapshot(symbol)
        self.memory.checkpoint(run_id, "snapshot", {"symbol": symbol, "bars": len(snap.ohlcv)})
        await self.events.publish("snapshot_ready", run_id, symbol=symbol, bars=len(snap.ohlcv))

        # ② 分析（并行 + 失败隔离）
        results = await asyncio.gather(*(a.run(snap, ctx) for a in ANALYSTS), return_exceptions=True)
        reports: list[AnalystReport] = []
        for a, r in zip(ANALYSTS, results):
            if isinstance(r, Exception):
                reports.append(AnalystReport(agent=a.id, stance="neutral", confidence=0.0,
                                             rationale=f"分析失败：{r}", degraded=True))
            else:
                reports.append(r)
        await self.events.publish("reports_ready", run_id, symbol=symbol,
                                  reports=[r.model_dump(mode="json") for r in reports])

        # 参考价 & 账户；模拟盘喂 mark
        ref = Decimal(str(snap.ohlcv[-1].close)) if snap.ohlcv else Decimal(0)
        if hasattr(self.venue, "set_mark_price") and ref > 0:
            self.venue.set_mark_price(symbol, ref)
        bal = await self.venue.balances()
        equity = bal.total_quote if bal.total_quote > 0 else bal.free_quote

        # ③ 决策
        proposal = await self.strategist.run(reports, snap, equity, cfg, ctx, venue_id=getattr(self.venue, "id", "paper"))
        self.memory.checkpoint(run_id, "proposal", proposal.model_dump(mode="json"))
        await self.events.publish("proposal_ready", run_id, symbol=symbol, proposal=proposal.model_dump(mode="json"))
        if proposal.side == "flat":
            return RunResult(run_id=run_id, symbol=symbol, status="no_trade", reports=reports, proposal=proposal)

        # ④ 风控引擎（硬护栏）+ ⑤ 风控官（软评审）
        assessment = await self._assess(symbol, run_id, proposal, reports, ref, equity, cfg, ctx)
        await self.events.publish("risk_assessed", run_id, symbol=symbol,
                                  assessment=assessment.model_dump(mode="json"))
        if assessment.verdict == "rejected":
            return RunResult(run_id=run_id, symbol=symbol, status="rejected",
                             reports=reports, proposal=proposal, assessment=assessment)

        # ⑥ 人工审批门
        decision = await self.approval.decide(proposal, assessment)
        await self.events.publish("approval_decided", run_id, symbol=symbol,
                                  decision=decision.model_dump(mode="json"))
        if decision.decision == "reject":
            return RunResult(run_id=run_id, symbol=symbol, status="not_approved",
                             reports=reports, proposal=proposal, assessment=assessment, decision=decision)

        final_prop = decision.modified or proposal
        final_size = assessment.adjusted_size_quote or final_prop.size_quote

        # ⑦ 执行（幂等 + 入场即挂保护单）
        execution = await self.trader.execute(final_prop, self.venue, client_id=run_id, size_quote=final_size)
        self.memory.checkpoint(run_id, "execution", execution.model_dump(mode="json"))
        await self.events.publish("executed", run_id, symbol=symbol, execution=execution.model_dump(mode="json"))

        # 复盘 + 经验回灌
        review = await self.reviewer.run(final_prop, execution, ctx, run_id=run_id)
        self.memory.append_lessons(review.lessons)
        await self.events.publish("reviewed", run_id, symbol=symbol, review=review.model_dump(mode="json"))

        return RunResult(run_id=run_id, symbol=symbol, status="executed", reports=reports,
                         proposal=final_prop, assessment=assessment, decision=decision,
                         execution=execution, review=review)

    async def _assess(self, symbol, run_id, proposal, reports, ref, equity, cfg, ctx) -> RiskAssessment:
        # preflight 估算滑点/爆仓价 → 喂给风控引擎
        pf_intent = OrderIntent(
            client_id=f"{run_id}-pf", symbol=symbol, venue=getattr(self.venue, "id", "paper"),
            side=proposal.side, instrument=proposal.instrument, order_type=proposal.entry.type,
            size_quote=proposal.size_quote, leverage=proposal.leverage,
            stop_loss=proposal.stop_loss, take_profit=proposal.take_profit,
        )
        pf = await self.venue.preflight(pf_intent)
        positions = await self.venue.positions()
        gross = sum((p.notional_at(ref) for p in positions), Decimal(0))
        symbol_exp = sum((p.notional_at(ref) for p in positions if p.symbol == symbol), Decimal(0))

        rctx = RiskContext(
            equity_quote=equity, ref_price=ref,
            gross_exposure_quote=gross, symbol_exposure_quote=symbol_exp,
            kill=self.settings.kill, reconciling=False,
            est_slippage_bps=pf.est_slippage_bps, est_liq_price=pf.est_liq_price,
        )
        assessment = risk_assess(proposal, rctx, cfg)
        return await self.risk_officer.run(proposal, assessment, reports, ctx)
