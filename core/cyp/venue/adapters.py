"""交易所适配层：消化各家 ccxt 抹不平的差异（保护单参数、持仓/保证金模式）。

CexVenue 的通用流程（幂等/preflight/入场/失败平裸仓）不变，仅把以下三件事委托适配器：
- configure_perp：设置杠杆 + 保证金模式
- entry_params：入场/平仓的下单参数（clientOrderId / reduceOnly / tdMode ...）
- place_protective：挂原生保护单（止损/止盈）
新增一家交易所 = 加一个适配器（见 docs/ARCHITECTURE.md「CEX 适配点」）。
"""

from __future__ import annotations

import contextlib
from decimal import Decimal

from cyp.contracts import OrderIntent, ProtectiveOrder


def _close_side(intent: OrderIntent) -> str:
    return "sell" if intent.side == "long" else "buy"


class BinanceAdapter:
    id = "binance"

    async def configure_perp(self, ex, intent: OrderIntent) -> None:
        for setter, arg in ((ex.set_margin_mode, intent.margin_mode),
                            (ex.set_leverage, int(intent.leverage))):
            with contextlib.suppress(Exception):  # 已设置过会报错，忽略
                await setter(arg, intent.symbol)

    def entry_params(self, intent: OrderIntent, is_close: bool) -> dict:
        params: dict = {"clientOrderId": intent.client_id}
        if intent.instrument == "perp":
            params["reduceOnly"] = is_close
        return params

    async def place_protective(self, ex, intent: OrderIntent, amount: Decimal) -> list[ProtectiveOrder]:
        cs = _close_side(intent)
        out: list[ProtectiveOrder] = []
        if intent.stop_loss is not None:
            o = await ex.create_order(intent.symbol, "STOP_MARKET", cs, float(amount), None,
                                      {"stopPrice": float(intent.stop_loss), "reduceOnly": True,
                                       "clientOrderId": f"{intent.client_id}-sl"})
            out.append(ProtectiveOrder(kind="stop_loss", order_id=str(o.get("id")),
                                       trigger_price=intent.stop_loss))
        for i, tp in enumerate(intent.take_profit):
            o = await ex.create_order(intent.symbol, "TAKE_PROFIT_MARKET", cs, float(amount), None,
                                      {"stopPrice": float(tp), "reduceOnly": True,
                                       "clientOrderId": f"{intent.client_id}-tp{i}"})
            out.append(ProtectiveOrder(kind="take_profit", order_id=str(o.get("id")), trigger_price=tp))
        return out


class OkxAdapter:
    """OKX：保证金模式走 tdMode（现货 cash / 合约 isolated|cross）；杠杆需带 mgnMode；
    保护单用 ccxt 统一的 stopLossPrice/takeProfitPrice（映射为 OKX 条件/算法单）。"""

    id = "okx"

    def _td_mode(self, intent: OrderIntent) -> str:
        return intent.margin_mode if intent.instrument == "perp" else "cash"

    async def configure_perp(self, ex, intent: OrderIntent) -> None:
        with contextlib.suppress(Exception):  # 已设置过会报错，忽略
            await ex.set_leverage(int(intent.leverage), intent.symbol, {"mgnMode": intent.margin_mode})

    def entry_params(self, intent: OrderIntent, is_close: bool) -> dict:
        params: dict = {"clientOrderId": intent.client_id, "tdMode": self._td_mode(intent)}
        if intent.instrument == "perp" and is_close:
            params["reduceOnly"] = True
        return params

    async def place_protective(self, ex, intent: OrderIntent, amount: Decimal) -> list[ProtectiveOrder]:
        cs = _close_side(intent)
        td = self._td_mode(intent)
        out: list[ProtectiveOrder] = []
        if intent.stop_loss is not None:
            o = await ex.create_order(intent.symbol, "market", cs, float(amount), None,
                                      {"stopLossPrice": float(intent.stop_loss), "reduceOnly": True,
                                       "tdMode": td, "clientOrderId": f"{intent.client_id}-sl"})
            out.append(ProtectiveOrder(kind="stop_loss", order_id=str(o.get("id")),
                                       trigger_price=intent.stop_loss))
        for i, tp in enumerate(intent.take_profit):
            o = await ex.create_order(intent.symbol, "market", cs, float(amount), None,
                                      {"takeProfitPrice": float(tp), "reduceOnly": True,
                                       "tdMode": td, "clientOrderId": f"{intent.client_id}-tp{i}"})
            out.append(ProtectiveOrder(kind="take_profit", order_id=str(o.get("id")), trigger_price=tp))
        return out


_ADAPTERS = {"binance": BinanceAdapter, "okx": OkxAdapter}


def get_adapter(exchange_id: str):
    """按交易所 id 取适配器；未知交易所回退 Binance 风格。"""
    return _ADAPTERS.get(exchange_id, BinanceAdapter)()
