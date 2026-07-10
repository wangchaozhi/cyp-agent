"""跨所行情聚合：最优场所 + 跨所价差。用假场所离线验证。"""

import asyncio
from decimal import Decimal

from cyp.venue import MarketAggregator

run = asyncio.run


class FakeVenue:
    def __init__(self, vid, price, funding=None):
        self.id = vid
        self._price = price
        self._funding = funding
        self.ticker_calls = 0
        self.funding_calls = 0

    async def fetch_ticker(self, symbol):
        self.ticker_calls += 1
        if self._price is None:
            raise RuntimeError("no ticker")
        return Decimal(str(self._price))

    async def fetch_funding_rate(self, symbol):
        self.funding_calls += 1
        return Decimal(str(self._funding)) if self._funding is not None else None


def test_tickers_skips_failures():
    agg = MarketAggregator([FakeVenue("a", 100), FakeVenue("b", None), FakeVenue("c", 102)])
    t = run(agg.tickers("BTC/USDT"))
    assert set(t) == {"a", "c"} and t["a"] == Decimal("100")


def test_tickers_fetch_venues_concurrently():
    async def scenario():
        ready = asyncio.Event()
        started = 0

        class CoordinatedVenue:
            def __init__(self, vid, price):
                self.id = vid
                self.price = Decimal(price)

            async def fetch_ticker(self, symbol):
                nonlocal started
                started += 1
                if started == 2:
                    ready.set()
                await asyncio.wait_for(ready.wait(), timeout=0.5)
                return self.price

        agg = MarketAggregator([CoordinatedVenue("a", "100"), CoordinatedVenue("b", "101")])
        return await agg.tickers("BTC/USDT")

    assert set(run(scenario())) == {"a", "b"}


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


def test_funding_rates_skip_unsupported():
    class NoFunding:
        id = "x"
        async def fetch_ticker(self, symbol):
            return Decimal("100")
    agg = MarketAggregator([FakeVenue("a", 100, funding="0.0001"), NoFunding()])
    rates = run(agg.funding_rates("BTC/USDT"))
    assert rates == {"a": Decimal("0.0001")}


def test_arb_hint_on_wide_spread():
    agg = MarketAggregator([FakeVenue("a", 100), FakeVenue("b", 101)])   # 100 bps
    hints = run(agg.arb_hints("BTC/USDT"))
    assert any("价差" in h for h in hints)


def test_arb_hint_on_funding_gap():
    agg = MarketAggregator([FakeVenue("a", 100, funding="0.0001"),
                            FakeVenue("b", 100, funding="0.0008")])
    hints = run(agg.arb_hints("BTC/USDT"))
    assert any("资金费差" in h for h in hints)


def test_no_hints_when_calm():
    agg = MarketAggregator([FakeVenue("a", 100, funding="0.0001"),
                            FakeVenue("b", "100.01", funding="0.00012")])
    assert run(agg.arb_hints("BTC/USDT")) == []


def test_summary_fetches_each_upstream_dimension_once():
    venues = [FakeVenue("a", 100, funding="0.0001"),
              FakeVenue("b", 101, funding="0.0008")]
    summary = run(MarketAggregator(venues).summary("BTC/USDT"))

    assert summary.best_buy == ("a", Decimal("100"))
    assert summary.best_sell == ("b", Decimal("101"))
    assert summary.spread_bps == Decimal("100")
    assert any("价差" in hint for hint in summary.arb_hints)
    assert all(v.ticker_calls == 1 and v.funding_calls == 1 for v in venues)
