"""尾部风险度量：Historical VaR / CVaR（Expected Shortfall）。

输入收益序列，输出非负损失比例或计价币金额。纯函数、零重依赖，便于风控单测。
约定：收益为小数（-0.02 = -2%）；损失为正数，盈利/不亏记为 0。
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal


@dataclass(frozen=True)
class TailRisk:
    confidence: float
    var_pct: float
    cvar_pct: float
    var_quote: Decimal
    cvar_quote: Decimal
    n: int
    degraded: bool = False
    reason: str = ""


def _check_confidence(confidence: float) -> None:
    if not 0 < confidence < 1:
        raise ValueError("confidence must be in (0, 1)")


def losses_from_returns(returns: list[float]) -> list[float]:
    """把收益转成非负损失。盈利或持平记 0，亏损取绝对值。"""

    return [max(0.0, -float(r)) for r in returns]


def quantile(values: list[float], q: float) -> float:
    """线性插值分位数。q in [0, 1]；空序列返回 0。"""

    if not values:
        return 0.0
    if q <= 0:
        return float(min(values))
    if q >= 1:
        return float(max(values))
    xs = sorted(float(v) for v in values)
    pos = (len(xs) - 1) * q
    lo = int(pos)
    hi = min(lo + 1, len(xs) - 1)
    frac = pos - lo
    return xs[lo] * (1 - frac) + xs[hi] * frac


def historical_var(returns: list[float], confidence: float = 0.95) -> float:
    """Historical VaR：给定置信度下的损失分位数（非负比例）。"""

    _check_confidence(confidence)
    return quantile(losses_from_returns(returns), confidence)


def conditional_value_at_risk(returns: list[float], confidence: float = 0.95) -> float:
    """CVaR / Expected Shortfall：超过 VaR 尾部的平均损失（非负比例）。"""

    _check_confidence(confidence)
    losses = losses_from_returns(returns)
    if not losses:
        return 0.0
    var = quantile(losses, confidence)
    tail = [loss for loss in losses if loss >= var]
    return sum(tail) / len(tail) if tail else var


def tail_risk_quote(
    returns: list[float],
    equity_quote: Decimal,
    confidence: float = 0.95,
    min_samples: int = 30,
) -> TailRisk:
    """返回 VaR/CVaR 的比例与计价币金额。

    样本不足时仍给出估计，但标记 degraded，供上层仪表盘/审批展示；风控规则可选择
    因样本不足跳过或要求人工复核。
    """

    var_pct = historical_var(returns, confidence)
    cvar_pct = conditional_value_at_risk(returns, confidence)
    degraded = len(returns) < min_samples
    reason = f"样本数 {len(returns)} < 最小样本 {min_samples}" if degraded else ""
    return TailRisk(
        confidence=confidence,
        var_pct=var_pct,
        cvar_pct=cvar_pct,
        var_quote=(equity_quote * Decimal(str(var_pct))).quantize(Decimal("0.01")),
        cvar_quote=(equity_quote * Decimal(str(cvar_pct))).quantize(Decimal("0.01")),
        n=len(returns),
        degraded=degraded,
        reason=reason,
    )
