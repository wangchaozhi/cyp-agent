"""OKX Demo 联网 smoke 实测（M2 真实网络项的 OKX 版验收，可重复执行）。

    python -m cyp.tools.okx_smoke [--size 10] [--symbol BTC/USDT]

流程：配置校验 → 行情/余额 → 小额现货下单（带止损/止盈保护单）→ 撤保护单
      → 平仓清理 → 对账（余额/持仓/残留检查）。

前提：.env 已配置 OKX_API_KEY / OKX_API_SECRET / OKX_PASSWORD（Demo Trading 凭据），
CYP_OKX_DEMO 保持默认 True。真实资金实盘绝不使用本脚本。
"""

from __future__ import annotations

import argparse
import asyncio
import time
from decimal import Decimal

from cyp.config import Settings
from cyp.console import configure_utf8_stdio
from cyp.contracts import OrderIntent
from cyp.venue import CexVenue

_SLIP = Decimal("10") / Decimal(10000)   # 与 CexVenue 默认 est_slippage_bps 一致


def _step(n: int, name: str, ok: bool, detail: str = "") -> bool:
    mark = "✅" if ok else "❌"
    print(f"  {mark} 步骤{n} {name}" + (f"：{detail}" if detail else ""))
    return ok


async def smoke(symbol: str, size_quote: Decimal) -> int:
    settings = Settings()
    print(f"OKX Demo smoke · {symbol} · 名义 {size_quote} USDT · demo={settings.okx_demo}")
    print("-" * 60)

    # ① 配置校验
    if not _step(1, "配置校验", settings.okx_configured and settings.okx_demo,
                 "OKX Demo 凭据齐全" if settings.okx_configured else "缺 OKX Key/Secret/Passphrase"):
        return 1

    venue = CexVenue(exchange_id="okx", api_key=settings.okx_api_key,
                     api_secret=settings.okx_api_secret, password=settings.okx_password,
                     read_only=False, sandbox=settings.okx_demo)
    base_ccy = symbol.split("/")[0]
    failures = 0
    try:
        # ② 行情 + 余额
        last = await venue.fetch_ticker(symbol)
        bal0 = await venue.balances()
        if not _step(2, "行情/余额", last > 0 and bal0.free_quote > size_quote,
                     f"last={last} USDT 可用={bal0.free_quote}"):
            return 1

        # 记录 base 币初始余额（Demo 账户可能自带历史持仓，对账只看本次增量）
        ex = venue._ccxt()  # noqa: SLF001 —— 实测脚本读原始余额做精确清理
        raw0 = await ex.fetch_balance()
        base_before = Decimal(str((raw0.get(base_ccy) or {}).get("free", 0)))

        # ③ 小额现货买入 + 原生保护单（止损 -10% / 止盈 +20%）
        client_id = f"smoke{int(time.time())}"
        intent = OrderIntent(
            client_id=client_id, symbol=symbol, venue="okx", side="long",
            instrument="spot", order_type="market", size_quote=size_quote,
            stop_loss=(last * Decimal("0.9")).quantize(Decimal("0.1")),
            take_profit=[(last * Decimal("1.2")).quantize(Decimal("0.1"))],
        )
        res = await venue.place(intent)
        ok3 = res.status == "filled" and len(res.protective_orders) == 2
        if not _step(3, "下单+保护单", ok3,
                     f"status={res.status} 均价={res.avg_price} 保护单={len(res.protective_orders)}"
                     + (f" 错误={res.error}" if res.error else "")):
            failures += 1
        # 幂等校验：同 client_id 重放返回同一结果、不重复成交
        replay = await venue.place(intent)
        if not _step(3, "幂等重放", replay is res, "同 client_id 未重复下单"):
            failures += 1

        # ④ 撤保护单
        ok4 = True
        for order in res.protective_orders:
            try:
                await venue.cancel(order.order_id)
            except Exception as e:  # noqa: BLE001
                ok4 = False
                print(f"     撤单失败 {order.kind} {order.order_id}: {e}")
        if not _step(4, "撤保护单", ok4, f"已撤 {len(res.protective_orders)} 张条件单"):
            failures += 1

        # ⑤ 平仓清理：只卖出本次买入的增量（扣除手续费后的实际到账）
        raw = await ex.fetch_balance()
        free_base = Decimal(str((raw.get(base_ccy) or {}).get("free", 0)))
        sell_base = min(free_base - base_before, res.filled_base)
        ok5 = True
        if sell_base > 0:
            close_intent = OrderIntent(
                client_id=f"{client_id}c", symbol=symbol, venue="okx", side="long",
                instrument="spot", order_type="market", reduce_only=True,
                size_quote=(sell_base * last * (Decimal(1) - _SLIP)).quantize(Decimal("0.0001")),
            )
            close_res = await venue.place(close_intent)
            ok5 = close_res.status == "filled"
            detail = f"卖出 {sell_base} {base_ccy}，status={close_res.status}"
            if close_res.error:
                detail += f" 错误={close_res.error}"
        else:
            detail = "无可清理余额"
        if not _step(5, "平仓清理", ok5, detail):
            failures += 1

        # ⑥ 对账：余额回流 + 本次买入无残留（增量对比，忽略账户历史持仓）
        bal1 = await venue.balances()
        raw1 = await ex.fetch_balance()
        base_after = Decimal(str((raw1.get(base_ccy) or {}).get("free", 0)))
        dust = base_after - base_before
        dust_quote = dust * last
        ok6 = dust_quote < size_quote * Decimal("0.05")   # 残留 <5% 视为清理干净（手续费/精度尘埃）
        if not _step(6, "对账", ok6,
                     f"USDT {bal0.free_quote} → {bal1.free_quote}，"
                     f"{base_ccy} 残留 {dust}（≈{dust_quote:.2f} USDT）"):
            failures += 1
    finally:
        await venue.close()

    print("-" * 60)
    print("结果：" + ("全部通过 ✅" if failures == 0 else f"{failures} 步失败 ❌"))
    return 0 if failures == 0 else 1


def main(argv: list[str] | None = None) -> int:
    configure_utf8_stdio()
    parser = argparse.ArgumentParser(prog="okx-smoke", description="OKX Demo 联网 smoke 实测")
    parser.add_argument("--symbol", default="BTC/USDT")
    parser.add_argument("--size", type=float, default=10.0, help="名义规模（USDT）")
    args = parser.parse_args(argv)
    return asyncio.run(smoke(args.symbol, Decimal(str(args.size))))


if __name__ == "__main__":
    raise SystemExit(main())
