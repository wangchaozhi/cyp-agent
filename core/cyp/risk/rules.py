"""硬护栏规则：每条一个纯函数，输入 (proposal, ctx, cfg)，输出 RuleResult。

约定：
- REJECT  = 一票否决（不可下单）。
- DOWNSIZE = 允许但必须缩到 max_size_quote 以内。
- OK      = 本条通过。
引擎汇总所有规则：任一 REJECT → rejected；否则若有 DOWNSIZE → downsized 到最小上限；否则 approved。
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal
from enum import Enum

from cyp.config import RiskConfig
from cyp.contracts import TradeProposal


class RuleAction(str, Enum):
    OK = "ok"
    REJECT = "reject"
    DOWNSIZE = "downsize"


@dataclass
class RuleResult:
    rule: str
    action: RuleAction
    reason: str = ""
    max_size_quote: Decimal | None = None  # DOWNSIZE 时的名义上限


@dataclass
class RiskContext:
    """判定所需的账户/组合/预检状态。由编排器在调用前组装。"""

    equity_quote: Decimal                       # 账户净值（计价币）
    ref_price: Decimal                          # 当前参考价（市价入场用它折算风险）
    gross_exposure_quote: Decimal = Decimal(0)  # 当前总名义敞口
    symbol_exposure_quote: Decimal = Decimal(0)  # 该标的当前名义敞口
    correlated_exposure_quote: Decimal | None = None  # 相关性簇内已有同向净敞口（不含本提案）
    portfolio_var_quote: Decimal | None = None   # 组合 Historical VaR（通常为含本提案的 projected）
    portfolio_cvar_quote: Decimal | None = None  # 组合 CVaR / Expected Shortfall（projected）
    orders_last_hour: int = 0
    consecutive_losses: int = 0
    daily_drawdown: Decimal = Decimal(0)        # 0.03 = 回撤 3%
    weekly_drawdown: Decimal = Decimal(0)
    total_drawdown: Decimal = Decimal(0)
    reconciling: bool = False                   # 对账未完成 → 冻结开仓
    kill: bool = False                          # Kill Switch
    margin_ratio: Decimal | None = None         # 账户维持保证金率（合约）
    # preflight 估算（可选，缺则跳过对应规则）
    est_slippage_bps: Decimal | None = None
    est_liq_price: Decimal | None = None        # 合约爆仓价估算
    est_price_impact: Decimal | None = None     # 链上价格冲击 0..1
    # 链上专项（§2.3；仅链上场所填，None = 跳过对应规则）
    onchain: bool = False                       # 本提案走链上场所
    approval_amount: Decimal | None = None      # 拟授权额度（None+链上 = 无需授权）
    approval_unlimited: bool = False            # 无限授权标记（一票否决）
    contract_address: str | None = None         # 目标路由/合约地址
    pool_tvl_usd: Decimal | None = None         # 目标池 TVL
    est_gas_quote: Decimal | None = None        # gas 成本估算（计价币）
    mev_protected: bool | None = None           # 是否走私有内存池/MEV 防护路由


def _ok(rule: str) -> RuleResult:
    return RuleResult(rule, RuleAction.OK)


def _is_open(p: TradeProposal) -> bool:
    """开仓 vs 平仓/减仓。平仓类动作放行大部分开仓护栏（要能退出）。"""
    return p.side in ("long", "short")


# ---- 始终生效（含平仓）------------------------------------------------------

def rule_kill_switch(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if ctx.kill and _is_open(p):
        return RuleResult("kill_switch", RuleAction.REJECT, "Kill Switch 已开启，拒绝新开仓")
    return _ok("kill_switch")


def rule_slippage(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if ctx.est_slippage_bps is not None and ctx.est_slippage_bps > cfg.max_slippage_bps:
        return RuleResult("slippage", RuleAction.REJECT,
                          f"预估滑点 {ctx.est_slippage_bps}bps > 上限 {cfg.max_slippage_bps}bps")
    return _ok("slippage")


def rule_price_impact(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if ctx.est_price_impact is not None and ctx.est_price_impact > cfg.max_price_impact:
        return RuleResult("price_impact", RuleAction.REJECT,
                          f"链上价格冲击 {ctx.est_price_impact} > 上限 {cfg.max_price_impact}")
    return _ok("price_impact")


# ---- 仅开仓生效 -------------------------------------------------------------

def rule_reconciling(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if ctx.reconciling and _is_open(p):
        return RuleResult("reconciling", RuleAction.REJECT, "对账未完成，冻结新开仓（仅允许减仓/平仓）")
    return _ok("reconciling")


def rule_stop_loss_required(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if not _is_open(p):
        return _ok("stop_loss_required")
    if p.stop_loss is None:
        return RuleResult("stop_loss_required", RuleAction.REJECT, "提案缺少止损，直接否决")
    # 止损方向校验：多头止损须低于参考价，空头止损须高于参考价
    if p.side == "long" and p.stop_loss >= ctx.ref_price:
        return RuleResult("stop_loss_required", RuleAction.REJECT, "多头止损价须低于当前价")
    if p.side == "short" and p.stop_loss <= ctx.ref_price:
        return RuleResult("stop_loss_required", RuleAction.REJECT, "空头止损价须高于当前价")
    return _ok("stop_loss_required")


def rule_per_trade_risk(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    """单笔风险 R = 名义规模 × 止损距离占比 ≤ 账户净值 × max_risk_per_trade。"""
    if not _is_open(p) or p.stop_loss is None or ctx.ref_price <= 0:
        return _ok("per_trade_risk")
    stop_frac = abs(ctx.ref_price - p.stop_loss) / ctx.ref_price
    if stop_frac <= 0:
        return RuleResult("per_trade_risk", RuleAction.REJECT, "止损距离为零，无法定风险")
    risk_quote = p.size_quote * stop_frac
    budget = ctx.equity_quote * cfg.max_risk_per_trade
    if risk_quote > budget:
        max_size = budget / stop_frac  # 缩到恰好满足单笔风险预算
        return RuleResult("per_trade_risk", RuleAction.DOWNSIZE,
                          f"单笔风险 {risk_quote:.2f} > 预算 {budget:.2f}，缩仓至 {max_size:.2f}",
                          max_size_quote=max_size)
    return _ok("per_trade_risk")


def rule_position_cap(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if not _is_open(p):
        return _ok("position_cap")
    cap = ctx.equity_quote * cfg.max_position_pct
    if p.size_quote > cap:
        return RuleResult("position_cap", RuleAction.DOWNSIZE,
                          f"单仓 {p.size_quote} > 上限 {cap}", max_size_quote=cap)
    return _ok("position_cap")


def rule_gross_exposure(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if not _is_open(p):
        return _ok("gross_exposure")
    cap = ctx.equity_quote * cfg.max_gross_exposure
    room = cap - ctx.gross_exposure_quote
    if room <= 0:
        return RuleResult("gross_exposure", RuleAction.REJECT, f"总敞口已达上限 {cap}，无新增空间")
    if p.size_quote > room:
        return RuleResult("gross_exposure", RuleAction.DOWNSIZE,
                          f"新增后超总敞口上限 {cap}，缩至剩余空间 {room}", max_size_quote=room)
    return _ok("gross_exposure")


def rule_symbol_concentration(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if not _is_open(p):
        return _ok("symbol_concentration")
    cap = ctx.equity_quote * cfg.max_symbol_concentration
    room = cap - ctx.symbol_exposure_quote
    if room <= 0:
        return RuleResult("symbol_concentration", RuleAction.REJECT, f"该标的集中度已达上限 {cap}")
    if p.size_quote > room:
        return RuleResult("symbol_concentration", RuleAction.DOWNSIZE,
                          f"超单标的集中度上限 {cap}，缩至 {room}", max_size_quote=room)
    return _ok("symbol_concentration")


def rule_correlated_exposure(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    """相关性簇内同向净敞口上限：避免在一篮子高相关资产上押过重同向（系统性风险）。"""
    if not _is_open(p) or ctx.correlated_exposure_quote is None:
        return _ok("correlated_exposure")
    cap = ctx.equity_quote * cfg.max_correlated_exposure
    room = cap - ctx.correlated_exposure_quote
    if room <= 0:
        return RuleResult("correlated_exposure", RuleAction.REJECT,
                          f"相关性簇同向敞口已达上限 {cap}")
    if p.size_quote > room:
        return RuleResult("correlated_exposure", RuleAction.DOWNSIZE,
                          f"超相关性簇同向敞口上限 {cap}，缩至剩余 {room}", max_size_quote=room)
    return _ok("correlated_exposure")


def rule_cvar_limit(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    """组合尾部风险护栏：Projected CVaR 不得超过账户净值 × max_cvar_pct。"""
    if not _is_open(p) or ctx.portfolio_cvar_quote is None:
        return _ok("cvar_limit")
    cap = ctx.equity_quote * cfg.max_cvar_pct
    if ctx.portfolio_cvar_quote > cap:
        return RuleResult("cvar_limit", RuleAction.REJECT,
                          f"组合 CVaR {ctx.portfolio_cvar_quote} > 上限 {cap}")
    return _ok("cvar_limit")


def rule_leverage(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if not _is_open(p):
        return _ok("leverage")
    if Decimal(str(p.leverage)) > cfg.max_leverage:
        return RuleResult("leverage", RuleAction.REJECT,
                          f"杠杆 {p.leverage}x > 上限 {cfg.max_leverage}x")
    return _ok("leverage")


def rule_liq_buffer(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    """合约：入场价到爆仓价的缓冲须 ≥ min_liq_buffer。"""
    if not _is_open(p) or p.instrument != "perp" or ctx.est_liq_price is None or ctx.ref_price <= 0:
        return _ok("liq_buffer")
    buffer = abs(ctx.ref_price - ctx.est_liq_price) / ctx.ref_price
    if buffer < cfg.min_liq_buffer:
        return RuleResult("liq_buffer", RuleAction.REJECT,
                          f"爆仓缓冲 {buffer:.3f} < 下限 {cfg.min_liq_buffer}")
    return _ok("liq_buffer")


def rule_margin_mode(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    """合约默认强制逐仓（风险隔离），避免单仓爆仓拖垮全账户。"""
    if _is_open(p) and p.instrument == "perp" and cfg.force_isolated and p.margin_mode != "isolated":
        return RuleResult("margin_mode", RuleAction.REJECT,
                          f"合约须逐仓，当前 {p.margin_mode}")
    return _ok("margin_mode")


def rule_maintenance_margin(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    """账户维持保证金率低于下限时，冻结新开合约仓。"""
    if (_is_open(p) and p.instrument == "perp" and ctx.margin_ratio is not None
            and ctx.margin_ratio < cfg.min_margin_ratio):
        return RuleResult("maintenance_margin", RuleAction.REJECT,
                          f"维持保证金率 {ctx.margin_ratio} < 下限 {cfg.min_margin_ratio}")
    return _ok("maintenance_margin")


def rule_order_rate(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if _is_open(p) and ctx.orders_last_hour >= cfg.max_orders_per_hour:
        return RuleResult("order_rate", RuleAction.REJECT,
                          f"近一小时下单 {ctx.orders_last_hour} 次 ≥ 上限 {cfg.max_orders_per_hour}")
    return _ok("order_rate")


def rule_consecutive_losses(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if _is_open(p) and ctx.consecutive_losses >= cfg.max_consecutive_losses:
        return RuleResult("consecutive_losses", RuleAction.REJECT,
                          f"连亏 {ctx.consecutive_losses} 次 ≥ {cfg.max_consecutive_losses}，进入冷静期")
    return _ok("consecutive_losses")


def rule_drawdown_circuit(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    if not _is_open(p):
        return _ok("drawdown_circuit")
    if ctx.total_drawdown >= cfg.max_drawdown_limit:
        return RuleResult("drawdown_circuit", RuleAction.REJECT,
                          f"总回撤 {ctx.total_drawdown} ≥ 熔断线 {cfg.max_drawdown_limit}，全面停手")
    if ctx.weekly_drawdown >= cfg.weekly_drawdown_limit:
        return RuleResult("drawdown_circuit", RuleAction.REJECT,
                          f"周回撤 {ctx.weekly_drawdown} ≥ {cfg.weekly_drawdown_limit}，冻结开仓")
    if ctx.daily_drawdown >= cfg.daily_drawdown_limit:
        return RuleResult("drawdown_circuit", RuleAction.REJECT,
                          f"日回撤 {ctx.daily_drawdown} ≥ {cfg.daily_drawdown_limit}，冻结开仓")
    return _ok("drawdown_circuit")


# ---- 链上专项（§2.3）——非链上提案（ctx.onchain=False）全部跳过 ----------------

def rule_infinite_approval(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    """禁无限授权：授权额度必须是精确交易额度（+小缓冲），uint256-max 类授权一票否决。"""
    if not ctx.onchain or not _is_open(p):
        return _ok("infinite_approval")
    if ctx.approval_unlimited:
        return RuleResult("infinite_approval", RuleAction.REJECT, "检测到无限授权（unlimited approve），禁止")
    if ctx.approval_amount is not None and ctx.approval_amount > p.size_quote * Decimal("1.05"):
        return RuleResult("infinite_approval", RuleAction.REJECT,
                          f"授权额度 {ctx.approval_amount} 远超交易额 {p.size_quote}（须精确额度）")
    return _ok("infinite_approval")


def rule_contract_whitelist(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    """合约白名单：只与经过审查的路由/合约交互（蜜罐/假币防线）。"""
    if not ctx.onchain or not _is_open(p):
        return _ok("contract_whitelist")
    allowed = cfg.contract_whitelist_set()
    addr = (ctx.contract_address or "").lower()
    if not addr or addr not in allowed:
        return RuleResult("contract_whitelist", RuleAction.REJECT,
                          f"合约 {ctx.contract_address or '未知'} 不在白名单")
    return _ok("contract_whitelist")


def rule_min_pool_tvl(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    """最小流动性：池 TVL 过低意味着高冲击 + 高操纵风险。"""
    if not ctx.onchain or not _is_open(p) or ctx.pool_tvl_usd is None:
        return _ok("min_pool_tvl")
    if ctx.pool_tvl_usd < cfg.min_pool_tvl:
        return RuleResult("min_pool_tvl", RuleAction.REJECT,
                          f"池 TVL {ctx.pool_tvl_usd} < 下限 {cfg.min_pool_tvl}")
    return _ok("min_pool_tvl")


def rule_gas_cap(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    """gas 上限：gas 成本超过上限或超过交易额一定比例都不划算/异常。"""
    if not ctx.onchain or ctx.est_gas_quote is None:
        return _ok("gas_cap")
    if ctx.est_gas_quote > cfg.max_gas_quote:
        return RuleResult("gas_cap", RuleAction.REJECT,
                          f"gas 成本 {ctx.est_gas_quote} > 上限 {cfg.max_gas_quote}")
    return _ok("gas_cap")


def rule_mev_route(p: TradeProposal, ctx: RiskContext, cfg: RiskConfig) -> RuleResult:
    """MEV 防护：要求走私有内存池/保护路由，避免被三明治。"""
    if not ctx.onchain or not _is_open(p) or not cfg.require_private_mempool:
        return _ok("mev_route")
    if ctx.mev_protected is False:
        return RuleResult("mev_route", RuleAction.REJECT, "未走 MEV 防护路由（私有内存池），拒绝")
    return _ok("mev_route")


# 规则执行顺序（否决类优先靠前，便于阅读违规列表）
ALL_RULES = [
    rule_kill_switch,
    rule_reconciling,
    rule_drawdown_circuit,
    rule_consecutive_losses,
    rule_order_rate,
    rule_stop_loss_required,
    rule_leverage,
    rule_liq_buffer,
    rule_margin_mode,
    rule_maintenance_margin,
    rule_slippage,
    rule_price_impact,
    rule_infinite_approval,
    rule_contract_whitelist,
    rule_min_pool_tvl,
    rule_gas_cap,
    rule_mev_route,
    rule_per_trade_risk,
    rule_position_cap,
    rule_gross_exposure,
    rule_symbol_concentration,
    rule_correlated_exposure,
    rule_cvar_limit,
]
