"""扫参择优：对一组 StrategyConfig 跑回测，按目标函数排序，选出最优策略配置。

三档统一下，回测用的就是实盘管线——扫出的最优配置可直接注入 Orchestrator(strategy=...)。
"""

from __future__ import annotations

import itertools
from typing import Callable

from pydantic import BaseModel

from cyp.agents import StrategyConfig
from cyp.backtest.engine import Backtester
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
