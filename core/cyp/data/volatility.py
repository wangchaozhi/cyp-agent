"""波动率估计：EWMA（RiskMetrics）——比 ATR 更有预测性，捕捉波动聚集。

σ²_t = λ·σ²_{t-1} + (1-λ)·r²_t。近期收益权重更高，波动骤升能更快反映。
用于：波动目标仓位（size ∝ 目标波动/预测波动）+ 波动自适应止损。
纯 Python，零重依赖；GARCH 见 QUANT.md Q2。
"""

from __future__ import annotations

import math

from cyp.contracts import Candle


def simple_returns(candles: list[Candle]) -> list[float]:
    closes = [float(c.close) for c in candles]
    return [closes[i] / closes[i - 1] - 1 for i in range(1, len(closes)) if closes[i - 1] > 0]


def ewma_volatility(returns: list[float], lam: float = 0.94) -> float:
    """EWMA 波动率（每周期 σ）。数据不足返回 0。"""
    if len(returns) < 2:
        return 0.0
    var = returns[0] ** 2
    for r in returns[1:]:
        var = lam * var + (1 - lam) * r * r
    return math.sqrt(var)


def realized_volatility(returns: list[float]) -> float:
    """等权历史波动率（对照用）。"""
    if len(returns) < 2:
        return 0.0
    mu = sum(returns) / len(returns)
    return math.sqrt(sum((r - mu) ** 2 for r in returns) / len(returns))


def ewma_vol_from_candles(candles: list[Candle], lam: float = 0.94) -> float:
    return ewma_volatility(simple_returns(candles), lam)
