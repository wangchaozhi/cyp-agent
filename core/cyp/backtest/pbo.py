"""PBO：回测过拟合概率（Combinatorially Purged Cross-Validation）。

思想：把时间分成 S 段，枚举所有「一半做样本内(IS)、一半做样本外(OOS)」的组合。
每次在 IS 上挑最优策略，看它在 OOS 上的相对排名。若 IS 最优在 OOS 常沦为中下游，
说明"最优"多半是过拟合噪声。PBO = 最优策略 OOS 排名低于中位数的组合占比。
参考：Bailey et al., "The Probability of Backtest Overfitting" (2015)。
"""

from __future__ import annotations

import math
from collections.abc import Callable
from itertools import combinations

from cyp.backtest.stats import sharpe


def pbo(strategy_returns: list[list[float]], s: int = 6,
        metric: Callable[[list[float]], float] = sharpe) -> float:
    """strategy_returns：N 个策略，各一条等长收益序列。返回 PBO ∈ [0,1]（越高越过拟合）。"""
    n_strat = len(strategy_returns)
    if n_strat < 2:
        return 0.0
    t = len(strategy_returns[0])
    s = max(2, s - (s % 2))                      # S 需为偶数
    bounds = [round(i * t / s) for i in range(s + 1)]
    chunks = [(bounds[i], bounds[i + 1]) for i in range(s)]

    def perf(strat: list[float], groups) -> float:
        seq: list[float] = []
        for g in groups:
            seq += strat[chunks[g][0]:chunks[g][1]]
        return metric(seq) if len(seq) > 1 else 0.0

    lambdas: list[float] = []
    for is_groups in combinations(range(s), s // 2):
        oos_groups = [g for g in range(s) if g not in is_groups]
        is_perf = [perf(strat, is_groups) for strat in strategy_returns]
        oos_perf = [perf(strat, oos_groups) for strat in strategy_returns]
        best = max(range(n_strat), key=lambda j: is_perf[j])   # IS 最优
        # best 在 OOS 的相对排名（0=最差 … 1=最好）
        rank = sorted(range(n_strat), key=lambda j: oos_perf[j]).index(best)
        w = (rank + 1) / (n_strat + 1)
        lambdas.append(math.log(w / (1 - w)))
    # λ ≤ 0 ⇔ OOS 排名低于中位数
    return sum(1 for lam in lambdas if lam <= 0) / len(lambdas)
