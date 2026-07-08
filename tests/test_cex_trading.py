"""CexVenue 实盘下单：只读拒单 / 幂等 / 原生保护单 / 保护失败即平裸仓 / 合约设置。

用可注入的假 ccxt 交易所离线驱动，不触网。
"""

import asyncio
from decimal import Decimal

from cyp.contracts import OrderIntent
from cyp.venue import CexVenue

run = asyncio.run


class FakeExchange:
    def __init__(self, fail_protective: bool = False) -> None:
        self.calls: list[dict] = []
        self.fail_protective = fail_protective
        self._n = 0

    async def fetch_ticker(self, symbol):
        return {"last": 60000}

    async def create_order(self, symbol, type, side, amount, price=None, params=None):
        params = params or {}
        self.calls.append({"type": type, "side": side, "amount": amount, "params": params})
        if self.fail_protective and type in ("STOP_MARKET", "TAKE_PROFIT_MARKET"):
            raise RuntimeError("protective rejected")
        self._n += 1
        return {"id": f"o{self._n}", "clientOrderId": params.get("clientOrderId"),
                "average": price or 60030, "filled": amount, "status": "closed", "fee": {"cost": 0.4}}

    async def set_leverage(self, lev, symbol): self.calls.append({"set_leverage": lev})
    async def set_margin_mode(self, mode, symbol): self.calls.append({"set_margin_mode": mode})
    async def cancel_order(self, oid, symbol=None): self.calls.append({"cancel": oid})


def _venue(fake, read_only=False):
    return CexVenue("binance", api_key="k", api_secret="s", read_only=read_only, client=fake)


def _intent(**over):
    base = {"client_id": "c1", "symbol": "BTC/USDT", "venue": "binance", "side": "long", "instrument": "spot",
                "size_quote": Decimal("1000"), "stop_loss": Decimal("58000"), "take_profit": [Decimal("64000")]}
    base.update(over)
    return OrderIntent(**base)


def test_read_only_rejects_order():
    v = _venue(FakeExchange(), read_only=True)
    res = run(v.place(_intent()))
    assert res.status == "rejected"


def test_place_with_native_protective_orders():
    fake = FakeExchange()
    res = run(_venue(fake).place(_intent()))
    assert res.status == "filled"
    assert {p.kind for p in res.protective_orders} == {"stop_loss", "take_profit"}
    types = [c["type"] for c in fake.calls]
    assert "market" in types and "STOP_MARKET" in types and "TAKE_PROFIT_MARKET" in types


def test_idempotent_place():
    fake = FakeExchange()
    v = _venue(fake)
    r1 = run(v.place(_intent(client_id="dup")))
    n_after = len(fake.calls)
    r2 = run(v.place(_intent(client_id="dup")))
    assert r1.order_id == r2.order_id
    assert len(fake.calls) == n_after            # 未重复下单


def test_protective_failure_flattens_naked_position():
    fake = FakeExchange(fail_protective=True)
    res = run(_venue(fake).place(_intent()))
    assert res.status == "failed"
    assert "平裸仓" in (res.error or "")
    # 应有一笔市价平仓（fail-safe），clientOrderId 以 -flat 标识
    flat = [c for c in fake.calls if c["type"] == "market"
            and str(c["params"].get("clientOrderId", "")).endswith("-flat")]
    assert len(flat) == 1


def test_perp_sets_leverage_and_margin_mode():
    fake = FakeExchange()
    run(_venue(fake).place(_intent(instrument="perp", leverage=3.0, margin_mode="isolated")))
    assert any("set_leverage" in c for c in fake.calls)
    assert any(c.get("set_margin_mode") == "isolated" for c in fake.calls)


def test_preflight_perp_liq_price():
    pf = run(_venue(FakeExchange()).preflight(_intent(instrument="perp", leverage=2.0)))
    assert pf.ok and pf.est_liq_price is not None and pf.est_liq_price < pf.est_price
