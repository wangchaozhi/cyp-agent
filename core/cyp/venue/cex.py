"""CexVenue：ccxt 统一接入中心化交易所（现货 + 永续）。

- 只读行情无需密钥（M0 起可用）。
- 实盘下单（M2）：幂等 clientOrderId、原生保护单（stop/tp reduce-only）、
  **保护单失败即市价平裸仓**（有仓必有保护，见 RUNTIME.md §2）。
- 参考实现 = Binance；每家交易所的保护单/持仓模式差异在此适配层消化。
- client 可注入（假交易所）便于离线测试。
"""

from __future__ import annotations

from datetime import datetime, timezone
from decimal import Decimal

from cyp.contracts import (
    Balances,
    Candle,
    ExecutionResult,
    OrderBook,
    OrderIntent,
    Position,
    ProtectiveOrder,
)
from cyp.observability import get_logger
from cyp.venue.base import PreflightReport, VenueCaps


class CexVenue:
    kind = "cex"

    def __init__(self, exchange_id: str = "binance", api_key: str | None = None,
                 api_secret: str | None = None, read_only: bool = True,
                 quote_ccy: str = "USDT", est_slippage_bps: Decimal = Decimal("10"),
                 client=None) -> None:
        self.id = exchange_id
        self._api_key = api_key
        self._api_secret = api_secret
        self.quote_ccy = quote_ccy
        self._est_slippage_bps = est_slippage_bps
        self.caps = VenueCaps(spot=True, perp=True, native_protective_orders=True, read_only=read_only)
        self._client = client            # 注入的假交易所（测试）或惰性构造的 ccxt
        self._fills: dict[str, ExecutionResult] = {}   # client_id -> 结果（幂等）
        self.log = get_logger("cex")

    def is_configured(self) -> bool:
        return True if self.caps.read_only else bool(self._api_key and self._api_secret)

    def _ccxt(self):
        if self._client is None:
            try:
                import ccxt.async_support as ccxt
            except ImportError as e:  # pragma: no cover
                raise RuntimeError("需要 ccxt：pip install ccxt") from e
            cls = getattr(ccxt, self.id)
            self._client = cls({"apiKey": self._api_key, "secret": self._api_secret,
                                "enableRateLimit": True})
        return self._client

    async def close(self) -> None:
        if self._client is not None and hasattr(self._client, "close"):
            await self._client.close()

    # ---- 行情（只读）------------------------------------------------------

    async def fetch_ticker(self, symbol: str) -> Decimal:
        t = await self._ccxt().fetch_ticker(symbol)
        return Decimal(str(t["last"]))

    async def fetch_ohlcv(self, symbol: str, timeframe: str = "1h", limit: int = 200) -> list[Candle]:
        rows = await self._ccxt().fetch_ohlcv(symbol, timeframe=timeframe, limit=limit)
        out: list[Candle] = []
        for ts, o, h, l, c, v in rows:
            out.append(Candle(ts=datetime.fromtimestamp(ts / 1000, tz=timezone.utc),
                              open=Decimal(str(o)), high=Decimal(str(h)), low=Decimal(str(l)),
                              close=Decimal(str(c)), volume=Decimal(str(v))))
        return out

    async def fetch_orderbook(self, symbol: str, depth: int = 20) -> OrderBook:
        ob = await self._ccxt().fetch_order_book(symbol, limit=depth)
        pairs = lambda rows: [(Decimal(str(p)), Decimal(str(s))) for p, s in rows]
        return OrderBook(bids=pairs(ob["bids"]), asks=pairs(ob["asks"]))

    # ---- 账户 --------------------------------------------------------------

    async def positions(self) -> list[Position]:
        if self.caps.read_only:
            return []
        try:
            raw = await self._ccxt().fetch_positions()
        except Exception:  # noqa: BLE001
            return []
        out: list[Position] = []
        for p in raw:
            contracts = p.get("contracts") or 0
            if not contracts:
                continue
            out.append(Position(symbol=p["symbol"], venue=self.id, side=p.get("side", "long"),
                                instrument="perp", size_base=Decimal(str(contracts)),
                                entry_price=Decimal(str(p.get("entryPrice") or 0)),
                                leverage=float(p.get("leverage") or 1)))
        return out

    async def balances(self) -> Balances:
        try:
            b = await self._ccxt().fetch_balance()
        except Exception:  # noqa: BLE001
            return Balances(quote_ccy=self.quote_ccy)
        free = Decimal(str((b.get(self.quote_ccy) or {}).get("free", 0)))
        total = Decimal(str((b.get(self.quote_ccy) or {}).get("total", free)))
        return Balances(quote_ccy=self.quote_ccy, free_quote=free, total_quote=total)

    # ---- 执行 --------------------------------------------------------------

    async def preflight(self, intent: OrderIntent) -> PreflightReport:
        try:
            last = await self.fetch_ticker(intent.symbol)
        except Exception as e:  # noqa: BLE001
            return PreflightReport(ok=False, est_price=Decimal(0), reasons=[f"行情不可用:{e}"])
        slip = self._est_slippage_bps / Decimal(10000)
        up = intent.side == "long" and not intent.reduce_only
        est = last * (Decimal(1) + slip) if up else last * (Decimal(1) - slip)
        liq: Decimal | None = None
        if intent.instrument == "perp" and intent.leverage > 0:
            inv = Decimal(1) / Decimal(str(intent.leverage))
            liq = est * (Decimal(1) - inv) if intent.side == "long" else est * (Decimal(1) + inv)
        return PreflightReport(ok=True, est_price=est, est_slippage_bps=self._est_slippage_bps, est_liq_price=liq)

    async def place(self, intent: OrderIntent) -> ExecutionResult:
        if self.caps.read_only:
            return ExecutionResult(client_id=intent.client_id, status="rejected", error="只读场所不可下单")
        if intent.client_id in self._fills:                     # 幂等
            return self._fills[intent.client_id]

        ex = self._ccxt()
        pf = await self.preflight(intent)
        if not pf.ok:
            return self._remember(intent, ExecutionResult(
                client_id=intent.client_id, status="rejected", error="; ".join(pf.reasons)))

        price = pf.est_price
        amount = float(intent.size_quote / price) if price > 0 else 0.0
        is_close = intent.reduce_only or intent.side == "flat"
        entry_side = ("sell" if intent.side == "long" else "buy") if is_close \
            else ("buy" if intent.side == "long" else "sell")

        if intent.instrument == "perp":
            for setter, arg in ((ex.set_margin_mode, intent.margin_mode),
                                (ex.set_leverage, int(intent.leverage))):
                try:
                    await setter(arg, intent.symbol)
                except Exception:  # noqa: BLE001 —— 已设置过会报错，忽略
                    pass

        params = {"clientOrderId": intent.client_id}
        if intent.instrument == "perp":
            params["reduceOnly"] = is_close
        otype = "market" if intent.order_type == "market" else "limit"
        try:
            order = await ex.create_order(intent.symbol, otype, entry_side, amount,
                                          float(intent.price) if intent.price else None, params)
        except Exception as e:  # noqa: BLE001
            self.log.error("entry_failed", symbol=intent.symbol, error=str(e))
            return self._remember(intent, ExecutionResult(
                client_id=intent.client_id, status="failed", error=f"下单失败:{e}"))

        filled = Decimal(str(order.get("filled", amount)))
        avg = Decimal(str(order.get("average") or price))
        fee = Decimal(str((order.get("fee") or {}).get("cost", 0)))

        protective: list[ProtectiveOrder] = []
        if not is_close and (intent.stop_loss or intent.take_profit):
            try:
                protective = await self._place_protective(ex, intent, filled)
            except Exception as e:  # noqa: BLE001
                # ★ 有仓必有保护：保护单失败 → 立即市价平掉裸仓
                self.log.error("protective_failed_flatten", symbol=intent.symbol, error=str(e))
                await self._flatten(ex, intent, filled)
                return self._remember(intent, ExecutionResult(
                    client_id=intent.client_id, order_id=str(order.get("id")), status="failed",
                    filled_base=Decimal(0), error=f"保护单失败已市价平裸仓:{e}"))

        return self._remember(intent, ExecutionResult(
            client_id=intent.client_id, order_id=str(order.get("id")), status="filled",
            filled_base=filled, avg_price=avg, fee_quote=fee, protective_orders=protective))

    async def _place_protective(self, ex, intent: OrderIntent, amount: Decimal) -> list[ProtectiveOrder]:
        close_side = "sell" if intent.side == "long" else "buy"
        out: list[ProtectiveOrder] = []
        if intent.stop_loss is not None:
            o = await ex.create_order(intent.symbol, "STOP_MARKET", close_side, float(amount), None,
                                      {"stopPrice": float(intent.stop_loss), "reduceOnly": True,
                                       "clientOrderId": f"{intent.client_id}-sl"})
            out.append(ProtectiveOrder(kind="stop_loss", order_id=str(o.get("id")),
                                       trigger_price=intent.stop_loss))
        for i, tp in enumerate(intent.take_profit):
            o = await ex.create_order(intent.symbol, "TAKE_PROFIT_MARKET", close_side, float(amount), None,
                                      {"stopPrice": float(tp), "reduceOnly": True,
                                       "clientOrderId": f"{intent.client_id}-tp{i}"})
            out.append(ProtectiveOrder(kind="take_profit", order_id=str(o.get("id")), trigger_price=tp))
        return out

    async def _flatten(self, ex, intent: OrderIntent, amount: Decimal) -> None:
        close_side = "sell" if intent.side == "long" else "buy"
        await ex.create_order(intent.symbol, "market", close_side, float(amount), None,
                              {"reduceOnly": True, "clientOrderId": f"{intent.client_id}-flat"})

    def _remember(self, intent: OrderIntent, res: ExecutionResult) -> ExecutionResult:
        self._fills[intent.client_id] = res
        return res

    async def cancel(self, order_id: str) -> None:
        await self._ccxt().cancel_order(order_id)
