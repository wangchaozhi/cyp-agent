"""指标计算 + 合成行情源。全部离线确定性。"""

import asyncio
from decimal import Decimal

from cyp.contracts import Candle
from cyp.data import SyntheticMarketData, indicator_snapshot
from cyp.data.indicators import atr, bollinger, ema, macd, rsi, sma


def _flat(n, price=100.0):
    return [Candle(ts=__import__("datetime").datetime.now(), open=Decimal(str(price)),
                   high=Decimal(str(price)), low=Decimal(str(price)),
                   close=Decimal(str(price)), volume=Decimal("1")) for _ in range(n)]


def test_sma_and_ema_basic():
    vals = [float(i) for i in range(1, 11)]  # 1..10
    assert sma(vals, 10) == 5.5
    assert sma(vals, 20) is None            # 数据不足
    assert ema(vals, 5) is not None


def test_rsi_all_gains_is_100():
    vals = [float(i) for i in range(1, 30)]  # 单调上涨
    assert rsi(vals, 14) == 100.0


def test_rsi_bounds():
    vals = [100 + (i % 5) for i in range(40)]
    r = rsi(vals, 14)
    assert r is not None and 0.0 <= r <= 100.0


def test_macd_and_bollinger_shapes():
    vals = [100 + i * 0.5 for i in range(60)]
    m = macd(vals)
    assert m is not None and len(m) == 2
    bb = bollinger(vals, 20)
    assert bb is not None and bb[0] <= bb[1] <= bb[2]   # lower ≤ mid ≤ upper


def test_atr_on_flat_is_zero():
    assert atr(_flat(30), 14) == 0.0


def test_insufficient_data_returns_none():
    assert macd([1.0, 2.0]) is None
    assert atr(_flat(3)) is None
    assert bollinger([1.0], 20) is None


def test_synthetic_source_is_deterministic():
    src = SyntheticMarketData()
    snap1 = asyncio.run(src.snapshot("BTC/USDT"))
    snap2 = asyncio.run(src.snapshot("BTC/USDT"))
    assert len(snap1.ohlcv) == 200
    assert [c.close for c in snap1.ohlcv] == [c.close for c in snap2.ohlcv]   # 同 seed 同输出
    assert snap1.derivatives is not None and snap1.sentiment is not None


def test_indicator_snapshot_over_synthetic():
    snap = asyncio.run(SyntheticMarketData().snapshot("BTC/USDT"))
    ind = indicator_snapshot(snap.ohlcv)
    assert ind["last_close"] is not None
    assert ind["rsi"] is not None and 0.0 <= ind["rsi"] <= 100.0
    assert ind["macd"] is not None and ind["atr"] is not None
