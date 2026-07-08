"""PaperVenue：内存模拟盘。零密钥、确定性撮合，是 M0 默认执行场所与降级兜底。

简化（M0）：
- 用外部喂入的 mark price 成交，滑点为固定 bps 的不利偏移。
- 现货记账完整；合约以 leverage 记 position 并估算爆仓价，PnL 记账简化。
- place() 按 client_id 幂等：同一 id 重复调用返回同一结果（崩溃重放不重复成交）。
"""

from __future__ import annotations

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
from cyp.venue.base import PreflightReport, VenueCaps


class PaperVenue:
    id = "paper"
    kind = "paper"

    def __init__(
        self,
        initial_quote: Decimal = Decimal("10000"),
        quote_ccy: str = "USDT",
        slippage_bps: Decimal = Decimal("5"),
        fee_rate: Decimal = Decimal("0.0004"),
    ) -> None:
        self.caps = VenueCaps(spot=True, perp=True, native_protective_orders=True, read_only=False)
        self.quote_ccy = quote_ccy
        self._free_quote = initial_quote
        self._slippage_bps = slippage_bps
        self._fee_rate = fee_rate
        self._marks: dict[str, Decimal] = {}
        self._positions: dict[tuple[str, str], Position] = {}   # (symbol, instrument) -> Position
        self._fills: dict[str, ExecutionResult] = {}            # client_id -> result（幂等）
        self._seq = 0

    def is_configured(self) -> bool:
        return True

    # ---- 行情 --------------------------------------------------------------

    def set_mark_price(self, symbol: str, price: Decimal) -> None:
        self._marks[symbol] = Decimal(price)

    async def fetch_ticker(self, symbol: str) -> Decimal:
        if symbol not in self._marks:
            raise KeyError(f"PaperVenue 无 {symbol} 的 mark price，请先 set_mark_price")
        return self._marks[symbol]

    async def fetch_ohlcv(self, symbol: str, timeframe: str = "1h", limit: int = 200) -> list[Candle]:
        # 模拟盘不合成真实 K 线；行情应由数据层从只读 CEX 拉取。返回空以满足协议。
        return []

    async def fetch_orderbook(self, symbol: str, depth: int = 20) -> OrderBook:
        return OrderBook()

    # ---- 账户 --------------------------------------------------------------

    async def positions(self) -> list[Position]:
        return list(self._positions.values())

    async def balances(self) -> Balances:
        equity = self._free_quote
        for pos in self._positions.values():
            mark = self._marks.get(pos.symbol, pos.entry_price)
            upnl = pos.size_base * (mark - pos.entry_price) if pos.side == "long" \
                else pos.size_base * (pos.entry_price - mark)
            if pos.instrument == "perp":
                margin = (pos.size_base * pos.entry_price) / Decimal(str(pos.leverage))
                equity += margin + upnl
            else:  # 现货：持仓市值
                equity += pos.size_base * pos.entry_price + upnl
        return Balances(quote_ccy=self.quote_ccy, free_quote=self._free_quote, total_quote=equity)

    # ---- 执行 --------------------------------------------------------------

    def _next_id(self, prefix: str) -> str:
        self._seq += 1
        return f"paper-{prefix}-{self._seq}"

    async def preflight(self, intent: OrderIntent) -> PreflightReport:
        mark = self._marks.get(intent.symbol)
        if mark is None or mark <= 0:
            return PreflightReport(ok=False, est_price=Decimal(0), reasons=["无 mark price"])
        slip = self._slippage_bps / Decimal(10000)
        # 不利滑点：买/多向上，卖/空向下
        adverse_up = intent.side == "long" and not intent.reduce_only
        est_price = mark * (Decimal(1) + slip) if adverse_up else mark * (Decimal(1) - slip)
        liq: Decimal | None = None
        if intent.instrument == "perp" and intent.leverage > 0:
            inv_lev = Decimal(1) / Decimal(str(intent.leverage))
            liq = est_price * (Decimal(1) - inv_lev) if intent.side == "long" else est_price * (Decimal(1) + inv_lev)
        return PreflightReport(ok=True, est_price=est_price, est_slippage_bps=self._slippage_bps, est_liq_price=liq)

    async def place(self, intent: OrderIntent) -> ExecutionResult:
        if intent.client_id in self._fills:      # 幂等：重放返回同一结果
            return self._fills[intent.client_id]

        pf = await self.preflight(intent)
        if not pf.ok:
            res = ExecutionResult(client_id=intent.client_id, status="rejected", error="; ".join(pf.reasons))
            self._fills[intent.client_id] = res
            return res

        price = pf.est_price
        size_base = intent.size_quote / price if price > 0 else Decimal(0)
        fee = intent.size_quote * self._fee_rate
        key = (intent.symbol, intent.instrument)

        if intent.reduce_only or intent.side == "flat":
            res = self._close(intent, key, price, fee)
        else:
            # 现货扣全额名义；合约只扣保证金 = 名义 / 杠杆
            cost = (intent.size_quote / Decimal(str(intent.leverage))
                    if intent.instrument == "perp" else intent.size_quote)
            self._free_quote -= cost + fee
            self._positions[key] = Position(
                symbol=intent.symbol, venue=self.id, side=intent.side, instrument=intent.instrument,
                size_base=size_base, entry_price=price, leverage=intent.leverage,
            )
            protective = self._make_protective(intent)
            res = ExecutionResult(
                client_id=intent.client_id, order_id=self._next_id("ord"), status="filled",
                filled_base=size_base, avg_price=price, fee_quote=fee,
                slippage_bps=self._slippage_bps, protective_orders=protective,
            )

        self._fills[intent.client_id] = res
        return res

    def _make_protective(self, intent: OrderIntent) -> list[ProtectiveOrder]:
        out: list[ProtectiveOrder] = []
        if intent.stop_loss is not None:
            out.append(ProtectiveOrder(kind="stop_loss", order_id=self._next_id("sl"),
                                       trigger_price=intent.stop_loss))
        for tp in intent.take_profit:
            out.append(ProtectiveOrder(kind="take_profit", order_id=self._next_id("tp"), trigger_price=tp))
        return out

    def _close(self, intent: OrderIntent, key, price: Decimal, fee: Decimal) -> ExecutionResult:
        pos = self._positions.pop(key, None)
        if pos is None:
            return ExecutionResult(client_id=intent.client_id, status="rejected", error="无持仓可平")
        upnl = pos.size_base * (price - pos.entry_price) if pos.side == "long" \
            else pos.size_base * (pos.entry_price - price)
        if pos.instrument == "perp":
            margin = (pos.size_base * pos.entry_price) / Decimal(str(pos.leverage))
            proceeds = margin + upnl        # 退回保证金 + 已实现盈亏
        else:
            proceeds = pos.size_base * pos.entry_price + upnl
        self._free_quote += proceeds - fee
        return ExecutionResult(
            client_id=intent.client_id, order_id=self._next_id("close"), status="filled",
            filled_base=pos.size_base, avg_price=price, fee_quote=fee, slippage_bps=self._slippage_bps,
        )

    async def cancel(self, order_id: str) -> None:
        return None
