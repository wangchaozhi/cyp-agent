"""风控引擎：汇总所有硬护栏规则，产出确定性的 RiskAssessment。

裁决优先级：
  任一 REJECT      → verdict=rejected（列出全部否决理由）
  否则任一 DOWNSIZE → verdict=downsized（缩到所有上限中的最小值）
  否则             → verdict=approved
risk_score 是引擎给的粗粒度基线（越接近各上限越高），LLM 风控官在其之上细化。
"""

from __future__ import annotations

from decimal import Decimal

from cyp.config import RiskConfig
from cyp.contracts import RiskAssessment, TradeProposal
from cyp.risk.rules import ALL_RULES, RiskContext, RuleAction


def _clip01(x: Decimal) -> float:
    return float(max(Decimal(0), min(Decimal(1), x)))


def _base_risk_score(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig, size: Decimal) -> float:
    """按对各上限的占用度取最大值，作为基线风险分。"""
    if p.side not in ("long", "short") or ctx.equity_quote <= 0:
        return 0.0
    ratios: list[Decimal] = []
    # 杠杆占用
    if cfg.max_leverage > 0:
        ratios.append(Decimal(str(p.leverage)) / cfg.max_leverage)
    # 单仓占用
    if cfg.max_position_pct > 0:
        ratios.append((size / ctx.equity_quote) / cfg.max_position_pct)
    # 单笔风险占用
    if p.stop_loss is not None and ctx.ref_price > 0 and cfg.max_risk_per_trade > 0:
        stop_frac = abs(ctx.ref_price - p.stop_loss) / ctx.ref_price
        risk = size * stop_frac
        budget = ctx.equity_quote * cfg.max_risk_per_trade
        if budget > 0:
            ratios.append(risk / budget)
    # 总敞口占用
    if cfg.max_gross_exposure > 0:
        cap = ctx.equity_quote * cfg.max_gross_exposure
        if cap > 0:
            ratios.append((ctx.gross_exposure_quote + size) / cap)
    # 尾部风险占用（若调用方提供 projected CVaR）
    if ctx.portfolio_cvar_quote is not None and cfg.max_cvar_pct > 0:
        cap = ctx.equity_quote * cfg.max_cvar_pct
        if cap > 0:
            ratios.append(ctx.portfolio_cvar_quote / cap)
    return _clip01(max(ratios)) if ratios else 0.0


def assess(proposal: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RiskAssessment:
    results = [rule(proposal, ctx, cfg) for rule in ALL_RULES]

    rejects = [r for r in results if r.action == RuleAction.REJECT]
    if rejects:
        return RiskAssessment(
            verdict="rejected",
            hard_violations=[f"{r.rule}: {r.reason}" for r in rejects],
            risk_score=1.0,
        )

    downsizes = [r for r in results if r.action == RuleAction.DOWNSIZE]
    if downsizes:
        caps = [r.max_size_quote for r in downsizes if r.max_size_quote is not None]
        adjusted = min([proposal.size_quote, *caps]) if caps else proposal.size_quote
        return RiskAssessment(
            verdict="downsized",
            hard_violations=[f"{r.rule}: {r.reason}" for r in downsizes],
            adjusted_size_quote=adjusted,
            risk_score=_base_risk_score(proposal, ctx, cfg, adjusted),
        )

    return RiskAssessment(
        verdict="approved",
        risk_score=_base_risk_score(proposal, ctx, cfg, proposal.size_quote),
    )
