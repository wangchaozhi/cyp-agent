"""策略参数化 + 扫参择优。离线确定性。"""

import asyncio
from decimal import Decimal

from cyp.agents import AgentContext, Strategist, StrategyConfig
from cyp.backtest import default_objective, grid, robust_sweep, sweep
from cyp.config import RiskConfig, Settings
from cyp.contracts import AnalystReport
from cyp.data import SyntheticMarketData
from cyp.llm import build_llm

run = asyncio.run
CFG = RiskConfig(_env_file=None)


def _ctx():
    s = Settings(_env_file=None)
    return AgentContext(llm=build_llm(s), settings=s)


def _bullish():
    return [AnalystReport(agent="technical", stance="bullish", confidence=0.9),
            AnalystReport(agent="derivatives", stance="bullish", confidence=0.8)]


def _snap():
    return run(SyntheticMarketData().snapshot("BTC/USDT"))


# ---- StrategyConfig 影响策略官 ---------------------------------------------

def test_default_config_unchanged_behavior():
    p = run(Strategist().run(_bullish(), _snap(), Decimal("10000"), CFG, _ctx()))
    assert p.side == "long" and p.stop_loss is not None


def test_high_enter_threshold_blocks_weak_signal():
    weak = [AnalystReport(agent="technical", stance="bullish", confidence=0.2)]
    s_low = Strategist(StrategyConfig(enter_threshold=0.05))
    s_high = Strategist(StrategyConfig(enter_threshold=0.9))
    assert run(s_low.run(weak, _snap(), Decimal("10000"), CFG, _ctx())).side in ("long", "flat")
    assert run(s_high.run(weak, _snap(), Decimal("10000"), CFG, _ctx())).side == "flat"


def test_larger_k_stop_widens_stop_distance():
    snap = _snap()
    ref = Decimal(str(snap.ohlcv[-1].close))
    p2 = run(Strategist(StrategyConfig(k_stop=Decimal("2"))).run(_bullish(), snap, Decimal("10000"), CFG, _ctx()))
    p4 = run(Strategist(StrategyConfig(k_stop=Decimal("4"))).run(_bullish(), snap, Decimal("10000"), CFG, _ctx()))
    assert (ref - p4.stop_loss) > (ref - p2.stop_loss)     # k 越大止损越远


# ---- 扫参择优 --------------------------------------------------------------

def test_grid_cartesian_product():
    configs = grid(enter_threshold=[0.1, 0.2], k_stop=[Decimal("2"), Decimal("3")])
    assert len(configs) == 4
    assert all(isinstance(c, StrategyConfig) for c in configs)


def test_default_objective_return_minus_drawdown():
    assert default_objective({"total_return": 0.10, "max_drawdown": 0.03}) == 0.07


def test_sweep_ranks_by_score_desc():
    candles = run(SyntheticMarketData(bars=140, drift=0.002).snapshot("BTC/USDT")).ohlcv
    configs = grid(enter_threshold=[0.08, 0.15], k_stop=[Decimal("2"), Decimal("3")])
    results = run(sweep(Settings(_env_file=None), "BTC/USDT", candles, configs, window=60))
    assert len(results) == len(configs)
    scores = [r.score for r in results]
    assert scores == sorted(scores, reverse=True)          # 已按分排序
    assert all("total_return" in r.metrics for r in results)


def test_robust_sweep_reports_oos_and_overfit_verdict():
    candles = run(SyntheticMarketData(bars=260, drift=0.002).snapshot("BTC/USDT")).ohlcv
    configs = grid(enter_threshold=[0.08, 0.12, 0.18], k_stop=[Decimal("2"), Decimal("3")])
    rr = run(robust_sweep(Settings(_env_file=None), "BTC/USDT", candles, configs, window=60))
    assert 0.0 <= rr.pbo <= 1.0
    assert 0.0 <= rr.deflated_sharpe <= 1.0
    assert rr.verdict in ("PASS", "REJECT(疑似过拟合)")
    assert "total_return" in rr.oos_metrics and "total_return" in rr.is_metrics
