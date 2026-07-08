"""OKX 模拟交易（Demo/sandbox）+ 交易所适配层差异。用假 OKX 交易所离线验证参数。"""

import asyncio
from decimal import Decimal

from cyp.config import Settings
from cyp.contracts import OrderIntent
from cyp.venue import CexVenue, build_registry
from cyp.venue.adapters import BinanceAdapter, OkxAdapter, get_adapter

run = asyncio.run


class FakeOkx:
    def __init__(self):
        self.calls: list[dict] = []
        self.sandbox = False
        self._n = 0

    def set_sandbox_mode(self, on):        # ccxt 同步方法
        self.sandbox = on

    async def fetch_ticker(self, symbol):
        return {"last": 60000}

    async def create_order(self, symbol, type, side, amount, price=None, params=None):
        params = params or {}
        self.calls.append({"type": type, "side": side, "params": params})
        self._n += 1
        return {"id": f"okx{self._n}", "average": price or 60030, "filled": amount,
                "status": "closed", "fee": {"cost": 0.3}}

    async def set_leverage(self, lev, symbol, params=None):
        self.calls.append({"set_leverage": lev, "params": params or {}})


def _okx(fake, sandbox=True):
    return CexVenue("okx", api_key="k", api_secret="s", password="p",
                    read_only=False, sandbox=sandbox, client=fake)


def _intent(**over):
    base = dict(client_id="c1", symbol="BTC/USDT", venue="okx", side="long", instrument="spot",
                size_quote=Decimal("1000"), stop_loss=Decimal("58000"), take_profit=[Decimal("64000")])
    base.update(over)
    return OrderIntent(**base)


def test_get_adapter_by_exchange():
    assert isinstance(get_adapter("okx"), OkxAdapter)
    assert isinstance(get_adapter("binance"), BinanceAdapter)
    assert isinstance(get_adapter("unknown"), BinanceAdapter)   # 回退


def test_okx_enables_sandbox_demo():
    fake = FakeOkx()
    run(_okx(fake).place(_intent()))
    assert fake.sandbox is True            # 模拟盘已开启


def test_okx_spot_uses_tdmode_cash_and_unified_stop_params():
    fake = FakeOkx()
    res = run(_okx(fake).place(_intent()))
    assert res.status == "filled"
    entry = fake.calls[0]
    assert entry["params"]["tdMode"] == "cash"        # 现货
    # 保护单用 OKX 风格的 stopLossPrice / takeProfitPrice
    sl = next(c for c in fake.calls if "stopLossPrice" in c["params"])
    tp = next(c for c in fake.calls if "takeProfitPrice" in c["params"])
    assert sl["params"]["reduceOnly"] and tp["params"]["reduceOnly"]
    assert {p.kind for p in res.protective_orders} == {"stop_loss", "take_profit"}


def test_okx_perp_sets_leverage_with_mgnmode_and_isolated_tdmode():
    fake = FakeOkx()
    run(_okx(fake).place(_intent(instrument="perp", leverage=3.0, margin_mode="isolated",
                                 take_profit=[])))
    lev = next(c for c in fake.calls if "set_leverage" in c)
    assert lev["params"]["mgnMode"] == "isolated"     # OKX 杠杆需带 mgnMode
    entry = fake.calls[next(i for i, c in enumerate(fake.calls) if c.get("type") == "market")]
    assert entry["params"]["tdMode"] == "isolated"


def test_registry_registers_okx_readonly_without_creds():
    reg = build_registry(Settings(_env_file=None))
    okx = next((d for d in reg.describe() if d["id"] == "okx"), None)
    assert okx is not None and okx["read_only"] is True   # 无 demo 凭据 → 只读


def test_registry_okx_tradable_with_demo_creds():
    s = Settings(_env_file=None, okx_api_key="k", okx_api_secret="s", okx_password="p")
    reg = build_registry(s)
    okx = next(d for d in reg.describe() if d["id"] == "okx")
    assert okx["read_only"] is False                      # 有 demo 凭据 → 可模拟下单
