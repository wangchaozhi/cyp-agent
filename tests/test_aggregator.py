"""跨所行情聚合：最优场所 + 跨所价差。用假场所离线验证。"""

import asyncio
from decimal import Decimal

from cyp.venue import MarketAggregator

run = asyncio.run


class FakeVenue:
    def __init__(self, vid, price):
        self.id = vid
        self._price = price
    async def fetch_ticker(self, symbol):
        if self._price is None:
            raise RuntimeError("no ticker")
        return Decimal(str(self._price))


def test_tickers_skips_failures():
    agg = MarketAggregator([FakeVenue("a", 100), FakeVenue("b", None), FakeVenue("c", 102)])
    t = run(agg.tickers("BTC/USDT"))
    assert set(t) == {"a", "c"} and t["a"] == Decimal("100")


def test_best_venue_buy_is_cheapest():
    agg = MarketAggregator([FakeVenue("a", 100), FakeVenue("b", 99), FakeVenue("c", 101)])
    vid, price = run(agg.best_venue("BTC/USDT", "long"))
    assert vid == "b" and price == Decimal("99")


def test_best_venue_sell_is_dearest():
    agg = MarketAggregator([FakeVenue("a", 100), FakeVenue("b", 99), FakeVenue("c", 101)])
    vid, price = run(agg.best_venue("BTC/USDT", "short"))
    assert vid == "c" and price == Decimal("101")


def test_spread_bps_cross_venue():
    agg = MarketAggregator([FakeVenue("a", 100), FakeVenue("b", 101)])
    # (101-100)/100 * 10000 = 100 bps
    assert run(agg.spread_bps("BTC/USDT")) == Decimal("100")


def test_spread_none_when_single_source():
    agg = MarketAggregator([FakeVenue("a", 100), FakeVenue("b", None)])
    assert run(agg.spread_bps("BTC/USDT")) is None
