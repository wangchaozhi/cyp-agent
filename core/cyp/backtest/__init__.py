"""回测引擎：同一套 分析→决策→风控 管线跑历史数据（回测/模拟/实盘三档统一）。"""

from cyp.backtest.data import HistoricalData
from cyp.backtest.engine import Backtester, BacktestReport
from cyp.backtest.metrics import compute_metrics
from cyp.backtest.optimize import (
    RobustResult,
    SweepResult,
    default_objective,
    grid,
    robust_sweep,
    sweep,
)
from cyp.backtest.pbo import pbo
from cyp.backtest.stats import deflated_sharpe, probabilistic_sharpe, sharpe
from cyp.backtest.validate import purged_kfold_splits, walk_forward_splits

__all__ = ["Backtester", "BacktestReport", "HistoricalData", "compute_metrics",
           "sweep", "grid", "SweepResult", "default_objective",
           "robust_sweep", "RobustResult",
           "sharpe", "probabilistic_sharpe", "deflated_sharpe",
           "walk_forward_splits", "purged_kfold_splits", "pbo"]
