"""指标计算 + 合成/真实行情源。全部离线确定性。"""

import asyncio
from datetime import datetime, timezone
from decimal import Decimal

from cyp.contracts import Candle, OrderBook
from cyp.data import SyntheticMarketData, indicator_snapshot
from cyp.data.indicators import atr, bollinger, ema, macd, rsi, sma
from cyp.data.market import CexMarketData


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


class _FakeCexVenue:
    """假 CEX venue：K线/盘口 + 衍生品维度（可制造单项失败）。"""

    id = "fakecex"

    def __init__(self, funding="0.0004", oi="1000000", lsr="1.2", funding_fails=False):
        self._funding = funding
        self._oi = oi
        self._lsr = lsr
        self._funding_fails = funding_fails

    async def fetch_ohlcv(self, symbol, timeframe="1h", limit=200):
        now = datetime.now(timezone.utc)
        return [Candle(ts=now, open=Decimal("100"), high=Decimal("101"),
                       low=Decimal("99"), close=Decimal("100"), volume=Decimal("1"))
                for _ in range(limit)]

    async def fetch_orderbook(self, symbol, depth=20):
        return OrderBook(bids=[(Decimal("99"), Decimal("1"))], asks=[(Decimal("101"), Decimal("1"))])

    async def fetch_funding_rate(self, symbol):
        if self._funding_fails:
            raise RuntimeError("boom")
        return Decimal(self._funding) if self._funding else None

    async def fetch_open_interest(self, symbol):
        return Decimal(self._oi) if self._oi else None

    async def fetch_long_short_ratio(self, symbol):
        return Decimal(self._lsr) if self._lsr else None


def test_cex_snapshot_fills_derivatives_for_perp():
    snap = asyncio.run(CexMarketData(_FakeCexVenue()).snapshot("BTC/USDT:USDT"))
    d = snap.derivatives
    assert d is not None
    assert d.funding_rate == Decimal("0.0004")
    assert d.open_interest == Decimal("1000000")
    assert d.long_short_ratio == Decimal("1.2")


def test_cex_snapshot_spot_has_no_derivatives():
    snap = asyncio.run(CexMarketData(_FakeCexVenue()).snapshot("BTC/USDT"))
    assert snap.derivatives is None


def test_cex_snapshot_degrades_when_funding_unavailable():
    # 资金费拉取失败 → 整段衍生品留 None（分析师按降级处理），但快照不报错
    snap = asyncio.run(CexMarketData(_FakeCexVenue(funding_fails=True)).snapshot("ETH/USDT:USDT"))
    assert snap.derivatives is None
    assert len(snap.ohlcv) == 200


def test_cex_snapshot_partial_derivatives_ok():
    # OI/多空比缺失不影响资金费主信号
    snap = asyncio.run(CexMarketData(_FakeCexVenue(oi=None, lsr=None)).snapshot("BTC/USDT:USDT"))
    d = snap.derivatives
    assert d is not None and d.funding_rate == Decimal("0.0004")
    assert d.open_interest is None and d.long_short_ratio is None
