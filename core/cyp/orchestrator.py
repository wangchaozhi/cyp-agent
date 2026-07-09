"""编排器：串联 7 步闭环，逐步发事件 + 落检查点，驱动风控反馈重议。

采集 → 分析(并行,失败隔离) → 决策 → 风控引擎(硬护栏) → 风控官(软评审)
     → [审批门] → 交易员(幂等执行,入场即挂保护单) → 复盘官(经验回灌)

依赖全部注入（data/venue/agents/llm/events/memory/approval/settings），不读全局单例。
"""

from __future__ import annotations

import asyncio
import time
import uuid
from decimal import Decimal
from typing import Literal

from pydantic import BaseModel

from cyp.agents import (
    ANALYSTS,
    AgentContext,
    Reviewer,
    RiskOfficer,
    Strategist,
    StrategyConfig,
    Trader,
)
from cyp.alerts import Alerter, build_alerter
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
from cyp.observability import RunMetrics, Trace, get_logger
from cyp.portfolio import CorrelationModel, PortfolioTracker, PortfolioView, aggregate_positions
from cyp.risk import assess as risk_assess
from cyp.risk.rules import RiskContext

RunStatus = Literal["no_trade", "rejected", "not_approved", "executed", "execution_failed", "error"]


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
        metrics: RunMetrics | None = None,
        portfolio: PortfolioTracker | None = None,
        alerter: Alerter | None = None,
        risk_venues: list | None = None,
        strategy: StrategyConfig | None = None,
    ) -> None:
        self.settings = settings
        self.data = data_source
        self.venue = venue
        self.risk_venues = risk_venues or [venue]   # 组合级风控聚合的场所（默认仅执行场所）
        self.corr = CorrelationModel()
        self.events = events or EventBus()
        self.memory = memory or MemoryStore()
        self.approval = approval or AutoApprove()
        self.llm = llm or build_llm(settings)
        self.metrics = metrics or RunMetrics()
        self.portfolio = portfolio or PortfolioTracker()
        self.alerter = alerter or build_alerter(settings)
        self.log = get_logger("orchestrator")
        self.reconciling = False   # 对账未完成时置 True → 风控引擎冻结新开仓
        self.strategist = Strategist(strategy)
        self.risk_officer = RiskOfficer()
        self.trader = Trader()
        self.reviewer = Reviewer()

    async def run_once(self, symbol: str, run_id: str | None = None) -> RunResult:
        run_id = run_id or uuid.uuid4().hex[:12]
        trace = Trace(run_id)
        ctx = AgentContext(llm=self.llm, settings=self.settings,
                           lessons=self.memory.get_lessons(symbol=symbol))
        self.log.info("run_start", run_id=run_id, symbol=symbol, mode=self.settings.mode)
        try:
            res = await self._run(symbol, run_id, ctx, trace)
        except Exception as e:  # noqa: BLE001 —— 单轮失败隔离，不击穿调用方
            self.log.error("run_failed", run_id=run_id, symbol=symbol, error=str(e))
            await self.events.publish("run_failed", run_id, symbol=symbol, error=str(e))
            await self.alerter.alert("error", "run_failed", run_id=run_id, symbol=symbol, error=str(e))
            res = RunResult(run_id=run_id, symbol=symbol, status="error", error=str(e))
        slip = res.execution.slippage_bps if res.execution else None
        self.metrics.record(res.status, slip)
        self.log.info("run_done", run_id=run_id, symbol=symbol, status=res.status, trace=trace.summary())
        await self.events.publish("run_done", run_id, symbol=symbol, status=res.status, trace=trace.summary())
        return res

    async def _run(self, symbol: str, run_id: str, ctx: AgentContext, trace: Trace) -> RunResult:
        cfg = self.settings.risk
        await self.events.publish("run_started", run_id, symbol=symbol)

        # ① 采集
        async with trace.span("collect"):
            snap = await self.data.snapshot(symbol)
        self.memory.checkpoint(run_id, "snapshot", {"symbol": symbol, "bars": len(snap.ohlcv)})
        await self.events.publish("snapshot_ready", run_id, symbol=symbol, bars=len(snap.ohlcv))

        # ② 分析（并行 + 失败隔离）
        async with trace.span("analyze"):
            results = await asyncio.gather(*(a.run(snap, ctx) for a in ANALYSTS), return_exceptions=True)
        reports: list[AnalystReport] = []
        for a, r in zip(ANALYSTS, results, strict=False):
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
        self.portfolio.update_equity(equity)   # 更新净值高水位（供回撤熔断）

        # 跨场所聚合持仓：策略官主动规避 + 风控引擎组合护栏共用
        positions = await aggregate_positions(self.risk_venues)

        # ③ 决策
        async with trace.span("strategize"):
            proposal = await self.strategist.run(reports, snap, equity, cfg, ctx,
                                                 venue_id=getattr(self.venue, "id", "paper"),
                                                 positions=positions)
        self.memory.checkpoint(run_id, "proposal", proposal.model_dump(mode="json"))
        await self.events.publish("proposal_ready", run_id, symbol=symbol, proposal=proposal.model_dump(mode="json"))
        if proposal.side == "flat":
            return RunResult(run_id=run_id, symbol=symbol, status="no_trade", reports=reports, proposal=proposal)

        # ④ 风控引擎（硬护栏）+ ⑤ 风控官（软评审）
        async with trace.span("risk"):
            assessment = await self._assess(symbol, run_id, proposal, reports, ref, equity, cfg, ctx,
                                            positions=positions)
        await self.events.publish("risk_assessed", run_id, symbol=symbol,
                                  assessment=assessment.model_dump(mode="json"))
        if assessment.verdict == "rejected":
            serious = [v for v in assessment.hard_violations if any(
                k in v for k in ("kill_switch", "drawdown_circuit", "consecutive_losses", "maintenance_margin"))]
            if serious:
                await self.alerter.alert("warning", "risk_circuit", run_id=run_id,
                                         symbol=symbol, violations=serious)
            return RunResult(run_id=run_id, symbol=symbol, status="rejected",
                             reports=reports, proposal=proposal, assessment=assessment)

        # ⑥ 人工审批门（计时 → SLO 审批时延）
        async with trace.span("approval"):
            _t0 = time.monotonic()
            decision = await self.approval.decide(proposal, assessment, run_id)
            self.metrics.record_approval_latency(time.monotonic() - _t0)
        await self.events.publish("approval_decided", run_id, symbol=symbol,
                                  decision=decision.model_dump(mode="json"))
        if decision.decision == "reject":
            return RunResult(run_id=run_id, symbol=symbol, status="not_approved",
                             reports=reports, proposal=proposal, assessment=assessment, decision=decision)

        final_prop = decision.modified or proposal
        final_size = final_prop.size_quote
        if assessment.adjusted_size_quote is not None:
            final_size = min(final_size, assessment.adjusted_size_quote)

        # ⑦ 执行（幂等 + 入场即挂保护单）
        async with trace.span("execute"):
            execution = await self.trader.execute(final_prop, self.venue, client_id=run_id, size_quote=final_size)
        if execution.status == "filled":
            self.portfolio.record_order()   # 计入下单频率（频率上限护栏）
        self.memory.checkpoint(run_id, "execution", execution.model_dump(mode="json"))
        await self.events.publish("executed", run_id, symbol=symbol, execution=execution.model_dump(mode="json"))

        # 复盘 + 经验回灌
        async with trace.span("review"):
            review = await self.reviewer.run(final_prop, execution, ctx, run_id=run_id)
        self.memory.append_lessons(review.lessons, symbol=symbol)
        await self.events.publish("reviewed", run_id, symbol=symbol, review=review.model_dump(mode="json"))

        status: RunStatus = "executed" if execution.status == "filled" else "execution_failed"
        if status == "execution_failed":
            await self.alerter.alert("error", "execution_failed", run_id=run_id,
                                     symbol=symbol, error=execution.error)

        return RunResult(run_id=run_id, symbol=symbol, status=status, reports=reports,
                         proposal=final_prop, assessment=assessment, decision=decision,
                         execution=execution, review=review)

    async def _assess(self, symbol, run_id, proposal, reports, ref, equity, cfg, ctx,
                      positions: list | None = None) -> RiskAssessment:
        # preflight 估算滑点/爆仓价 → 喂给风控引擎
        pf_intent = OrderIntent(
            client_id=f"{run_id}-pf", symbol=symbol, venue=getattr(self.venue, "id", "paper"),
            side=proposal.side, instrument=proposal.instrument, order_type=proposal.entry.type,
            size_quote=proposal.size_quote, leverage=proposal.leverage,
            stop_loss=proposal.stop_loss, take_profit=proposal.take_profit,
        )
        pf = await self.venue.preflight(pf_intent)

        # 链上场所：拉授权/白名单/TVL/gas/MEV 上下文喂给 §2.3 规则
        onchain_ctx: dict = {}
        if getattr(self.venue, "kind", "") == "onchain":
            try:
                onchain_ctx = await self.venue.quote_context(pf_intent)
            except Exception as e:  # noqa: BLE001 —— 拉不到上下文按最保守处理
                onchain_ctx = {"onchain": True, "contract_address": None,
                               "mev_protected": False}
                self.log.warning("onchain_context_failed", error=str(e))

        # 跨场所聚合持仓 → 组合级敞口（当前标的用 ref，其它标的用入场价近似）
        if positions is None:
            positions = await aggregate_positions(self.risk_venues)
        view = PortfolioView(positions, self.corr)
        def resolve(s):
            return ref if s == symbol else None
        gross = view.gross_notional(resolve)
        symbol_exp = view.symbol_notional(symbol, resolve)
        correlated = view.cluster_net_directional(self.corr.cluster_of(symbol), proposal.side, resolve)

        # 合约维持保证金率 = 账户净值 / 现有永续名义（无永续仓则 None，规则跳过）
        perp_notional = sum((p.notional_at(ref if p.symbol == symbol else p.entry_price)
                             for p in positions if p.instrument == "perp"), Decimal(0))
        margin_ratio = (equity / perp_notional) if perp_notional > 0 else None

        psnap = self.portfolio.risk_snapshot(equity)
        rctx = RiskContext(
            equity_quote=equity, ref_price=ref,
            gross_exposure_quote=gross, symbol_exposure_quote=symbol_exp,
            kill=self.settings.kill, reconciling=self.reconciling, margin_ratio=margin_ratio,
            correlated_exposure_quote=correlated,
            orders_last_hour=psnap["orders_last_hour"],
            consecutive_losses=psnap["consecutive_losses"],
            daily_drawdown=psnap["daily_drawdown"],
            weekly_drawdown=psnap["weekly_drawdown"],
            total_drawdown=psnap["total_drawdown"],
            est_slippage_bps=pf.est_slippage_bps, est_liq_price=pf.est_liq_price,
            est_price_impact=pf.est_price_impact,
            **onchain_ctx,
        )
        assessment = risk_assess(proposal, rctx, cfg)
        return await self.risk_officer.run(proposal, assessment, reports, ctx)
