"""PaperVenue 撮合/幂等/记账 + registry 描述。全部离线确定性。"""

import asyncio
from decimal import Decimal

from cyp.config import Settings
from cyp.contracts import OrderIntent
from cyp.venue import PaperVenue, build_registry


def run(coro):
    return asyncio.run(coro)


def _open_intent(cid="c1", **over):
    base = {"client_id": cid, "symbol": "BTC/USDT", "venue": "paper", "side": "long",
                "instrument": "spot", "size_quote": Decimal("1000"), "stop_loss": Decimal("90")}
    base.update(over)
    return OrderIntent(**base)


def test_preflight_adverse_slippage():
    v = PaperVenue()
    v.set_mark_price("BTC/USDT", Decimal("100"))
    pf = run(v.preflight(_open_intent()))
    assert pf.ok
    assert pf.est_price > Decimal("100")           # 买入不利滑点向上
    assert pf.est_slippage_bps == Decimal("5")


def test_preflight_no_mark_fails():
    v = PaperVenue()
    pf = run(v.preflight(_open_intent()))
    assert not pf.ok


def test_place_open_creates_position_and_protective():
    v = PaperVenue(initial_quote=Decimal("10000"))
    v.set_mark_price("BTC/USDT", Decimal("100"))
    res = run(v.place(_open_intent(take_profit=[Decimal("120")])))
    assert res.status == "filled"
    assert res.filled_base > 0
    kinds = {p.kind for p in res.protective_orders}
    assert kinds == {"stop_loss", "take_profit"}   # 入场即挂保护单
    positions = run(v.positions())
    assert len(positions) == 1 and positions[0].side == "long"
    bal = run(v.balances())
    assert bal.free_quote < Decimal("10000")        # 扣了名义 + 手续费


def test_place_is_idempotent():
    v = PaperVenue()
    v.set_mark_price("BTC/USDT", Decimal("100"))
    r1 = run(v.place(_open_intent(cid="dup")))
    free_after_first = run(v.balances()).free_quote
    r2 = run(v.place(_open_intent(cid="dup")))       # 同 client_id 重放
    assert r1.order_id == r2.order_id
    assert run(v.balances()).free_quote == free_after_first   # 未重复扣款
    assert len(run(v.positions())) == 1                       # 未重复建仓


def test_close_removes_position():
    v = PaperVenue()
    v.set_mark_price("BTC/USDT", Decimal("100"))
    run(v.place(_open_intent(cid="open")))
    close = OrderIntent(client_id="close", symbol="BTC/USDT", venue="paper", side="long",
                        size_quote=Decimal("0"), reduce_only=True)
    res = run(v.place(close))
    assert res.status == "filled"
    assert len(run(v.positions())) == 0


def test_registry_describe():
    reg = build_registry(Settings(_env_file=None))
    ids = {d["id"] for d in reg.describe()}
    assert "paper" in ids and "binance" in ids
    paper = next(d for d in reg.describe() if d["id"] == "paper")
    assert paper["configured"] is True and paper["read_only"] is False
