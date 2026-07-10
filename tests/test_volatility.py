"""EWMA 波动率 + 策略官波动目标仓位/波动自适应止损。纯 Python 确定性。"""

import asyncio
from datetime import datetime, timezone
from decimal import Decimal

from cyp.agents import AgentContext, Strategist, StrategyConfig
from cyp.config import RiskConfig, Settings
from cyp.contracts import AnalystReport, Candle
from cyp.data import ewma_volatility, realized_volatility, simple_returns
from cyp.llm import build_llm

run = asyncio.run
CFG = RiskConfig(_env_file=None)


def test_ewma_zero_on_flat():
    assert ewma_volatility([0.0, 0.0, 0.0, 0.0]) == 0.0


def test_ewma_higher_for_more_volatile():
    calm = [0.001, -0.001, 0.001, -0.001] * 10
    wild = [0.05, -0.05, 0.05, -0.05] * 10
    assert ewma_volatility(wild) > ewma_volatility(calm)


def test_ewma_reacts_to_recent_spike_more_than_equal_weight():
    # 尾部突然放大波动：EWMA(近期加权) 应高于等权历史波动
    rets = [0.001] * 40 + [0.08, -0.08, 0.08, -0.08]
    assert ewma_volatility(rets) > realized_volatility(rets)


def _candles(prices):
    return [Candle(ts=datetime.now(timezone.utc), open=Decimal(str(p)), high=Decimal(str(p * 1.01)),
                   low=Decimal(str(p * 0.99)), close=Decimal(str(p)), volume=Decimal("1")) for p in prices]


def _snap_uptrend():
    from cyp.contracts import MarketSnapshot
    prices = [50000 + i * 100 for i in range(80)]
    return MarketSnapshot(symbol="BTC/USDT", venue="x", ohlcv=_candles(prices))


def _ctx():
    s = Settings(_env_file=None)
    return AgentContext(llm=build_llm(s), settings=s)


def _bull():
    return [AnalystReport(agent="technical", stance="bullish", confidence=0.9),
            AnalystReport(agent="derivatives", stance="bullish", confidence=0.8)]


def test_vol_stop_mode_differs_from_atr():
    snap = _snap_uptrend()
    p_atr = run(Strategist(StrategyConfig(stop_mode="atr")).run(_bull(), snap, Decimal("10000"), CFG, _ctx()))
    p_vol = run(Strategist(StrategyConfig(stop_mode="vol")).run(_bull(), snap, Decimal("10000"), CFG, _ctx()))
    assert p_atr.stop_loss != p_vol.stop_loss           # 波动度量不同 → 止损不同


def test_vol_target_sizes_by_target_over_sigma():
    snap = _snap_uptrend()
    sigma = ewma_volatility(simple_returns(snap.ohlcv))
    # 放宽单仓上限，让波动目标公式成为约束项
    cfg = RiskConfig(_env_file=None, max_position_pct=Decimal("100"))
    p = run(Strategist(StrategyConfig(vol_target=0.02)).run(_bull(), snap, Decimal("10000"), cfg, _ctx()))
    expected = Decimal("10000") * Decimal("0.02") / Decimal(str(sigma)) * Decimal("0.995")
    assert abs(p.size_quote - expected.quantize(Decimal("0.01"))) < Decimal("1")


def test_vol_target_size_clamped_by_position_cap():
    snap = _snap_uptrend()
    p = run(Strategist(StrategyConfig(vol_target=0.02)).run(_bull(), snap, Decimal("10000"), CFG, _ctx()))
    assert p.size_quote <= Decimal("10000") * CFG.max_position_pct
