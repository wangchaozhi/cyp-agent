"""扫参择优：对一组 StrategyConfig 跑回测，按目标函数排序，选出最优策略配置。

三档统一下，回测用的就是实盘管线——扫出的最优配置可直接注入 Orchestrator(strategy=...)。
"""

from __future__ import annotations

import itertools
from typing import Callable

from pydantic import BaseModel

from cyp.agents import StrategyConfig
from cyp.backtest.engine import Backtester
from cyp.backtest.pbo import pbo
from cyp.backtest.stats import deflated_sharpe, sharpe
from cyp.config import Settings
from cyp.contracts import Candle


class SweepResult(BaseModel):
    config: StrategyConfig
    metrics: dict
    score: float


def default_objective(m: dict) -> float:
    """奖励收益、惩罚回撤（简单可解释）。"""
    return round(m["total_return"] - m["max_drawdown"], 4)


def grid(**options) -> list[StrategyConfig]:
    """笛卡尔积生成候选配置：grid(enter_threshold=[...], k_stop=[...], ...)。"""
    keys = list(options)
    return [StrategyConfig(**dict(zip(keys, combo)))
            for combo in itertools.product(*(options[k] for k in keys))]


async def sweep(settings: Settings, symbol: str, candles: list[Candle],
                configs: list[StrategyConfig], window: int = 60,
                objective: Callable[[dict], float] = default_objective) -> list[SweepResult]:
    results: list[SweepResult] = []
    for cfg in configs:
        report = await Backtester(settings, symbol, candles, window, strategy=cfg).run()
        results.append(SweepResult(config=cfg, metrics=report.metrics, score=objective(report.metrics)))
    results.sort(key=lambda r: r.score, reverse=True)
    return results


def _bar_returns(equity_curve: list[float]) -> list[float]:
    return [equity_curve[i] / equity_curve[i - 1] - 1
            for i in range(1, len(equity_curve)) if equity_curve[i - 1] > 0]


class RobustResult(BaseModel):
    best_config: StrategyConfig
    is_metrics: dict            # 样本内（仅参考）
    oos_metrics: dict           # 样本外（真相）
    pbo: float                  # 过拟合概率（越低越好）
    deflated_sharpe: float      # 扣除多试验运气后的夏普显著性
    verdict: str                # PASS / REJECT(疑似过拟合)


async def robust_sweep(settings: Settings, symbol: str, candles: list[Candle],
                       configs: list[StrategyConfig], window: int = 60, oos_frac: float = 0.3,
                       objective: Callable[[dict], float] = default_objective,
                       pbo_max: float = 0.5, dsr_min: float = 0.5) -> RobustResult:
    """诚实择优：样本内(IS)挑最优 → 样本外(OOS)验证 + PBO + Deflated Sharpe 出裁决。

    不再"样本内挑最高分"——那是过拟合。IS 只用来选，OOS 才算数。
    """
    n = len(candles)
    split = max(window + 2, int(n * (1 - oos_frac)))
    is_candles = candles[:split]
    oos_candles = candles[split - window:]      # 保留 window 预热

    is_reports = [await Backtester(settings, symbol, is_candles, window, strategy=c).run() for c in configs]
    is_scores = [objective(r.metrics) for r in is_reports]
    is_curves = [_bar_returns(r.equity_curve) for r in is_reports]
    best_i = max(range(len(configs)), key=lambda i: is_scores[i])

    usable = min((len(c) for c in is_curves if len(c) > 1), default=0)
    matrix = [c[:usable] for c in is_curves if len(c) >= usable > 1]
    pbo_val = pbo(matrix, s=6) if usable >= 12 and len(matrix) >= 2 else 0.0
    trial_sharpes = [sharpe(c) for c in matrix] or [0.0]

    oos_rep = await Backtester(settings, symbol, oos_candles, window, strategy=configs[best_i]).run()
    oos_ret = _bar_returns(oos_rep.equity_curve)
    dsr = deflated_sharpe(oos_ret, trial_sharpes) if len(oos_ret) > 1 else 0.0

    passed = pbo_val <= pbo_max and oos_rep.metrics["total_return"] > 0 and dsr >= dsr_min
    return RobustResult(
        best_config=configs[best_i], is_metrics=is_reports[best_i].metrics,
        oos_metrics=oos_rep.metrics, pbo=round(pbo_val, 4), deflated_sharpe=round(dsr, 4),
        verdict="PASS" if passed else "REJECT(疑似过拟合)",
    )
