"""OnchainVenue：EVM 链上 DEX swap 场所（M3）。

设计与 CexVenue 同款：client 可注入（测试用 mock EVM client，离线全覆盖），
真实实现走 web3（可选依赖 pip install .[onchain]）。

安全要点：
- 「精确额度 approve → swap」两步，绝不无限授权。
- nonce 本地管理 + 链上对齐；tx 确认跟踪 + revert 处理；client_id 幂等去重。
- 无原生保护单：止损依赖监控循环存活（caps.native_protective_orders=False）。

client 协议（鸭子类型，mock 按此实现）：
    async get_nonce(address) -> int
    async quote_swap(symbol, side, size_quote) -> dict:
        {price, price_impact, pool_tvl_usd, router, gas_quote, mev_protected}
    async allowance(symbol, router) -> Decimal
    async send_approve(symbol, router, amount, nonce) -> tx_hash
    async send_swap(symbol, side, size_quote, min_out, nonce) -> tx_hash
    async wait_receipt(tx_hash) -> {"status": 1|0, "gas_used_quote": Decimal}
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
)
from cyp.observability import get_logger
from cyp.venue.base import PreflightReport, VenueCaps


class OnchainVenue:
    kind = "onchain"

    def __init__(self, chain: str = "ethereum", rpc_url: str | None = None,
                 signer=None, client=None, quote_ccy: str = "USDC",
                 initial_quote: Decimal = Decimal("0")) -> None:
        self.id = f"onchain-{chain}"
        self.chain = chain
        self._rpc_url = rpc_url
        self._signer = signer
        self._client = client                       # 注入 mock（测试）或惰性构造 web3 client
        self.quote_ccy = quote_ccy
        self.caps = VenueCaps(spot=True, perp=False, native_protective_orders=False,
                              read_only=client is None and rpc_url is None)
        self._free_quote = initial_quote
        self._positions: dict[str, Position] = {}   # symbol -> Position（链上仅现货多头）
        self._fills: dict[str, ExecutionResult] = {}  # client_id 幂等
        self._nonce: int | None = None              # 本地 nonce 游标（对账时与链上对齐）
        self._pending_txs: dict[str, str] = {}      # tx_hash -> 用途（确认跟踪）
        self.log = get_logger("onchain")

    def is_configured(self) -> bool:
        return self._client is not None or bool(self._rpc_url and self._signer)

    def _ensure_client(self):
        if self._client is None:
            raise RuntimeError("OnchainVenue 未配置：需注入 client 或提供 RPC + 签名器"
                               "（pip install .[onchain]）")
        return self._client

    # ---- 行情（链上场所以 DEX 报价为准） -------------------------------------

    async def fetch_ticker(self, symbol: str) -> Decimal:
        q = await self._ensure_client().quote_swap(symbol, "long", Decimal(1))
        return Decimal(str(q["price"]))

    async def fetch_ohlcv(self, symbol: str, timeframe: str = "1h", limit: int = 200) -> list[Candle]:
        return []   # 链上历史 K 线走数据管线（The Graph 等），场所本身不提供

    async def fetch_orderbook(self, symbol: str, depth: int = 20) -> OrderBook:
        return OrderBook()  # AMM 无订单簿

    # ---- 账户 ----------------------------------------------------------------

    async def positions(self) -> list[Position]:
        return list(self._positions.values())

    async def balances(self) -> Balances:
        equity = self._free_quote
        for pos in self._positions.values():
            equity += pos.size_base * pos.entry_price
        return Balances(quote_ccy=self.quote_ccy, free_quote=self._free_quote, total_quote=equity)

    # ---- nonce 管理 + 对账钩子 -------------------------------------------------

    async def _next_nonce(self) -> int:
        client = self._ensure_client()
        if self._nonce is None:
            self._nonce = await client.get_nonce(getattr(self._signer, "address", "0x0"))
        n = self._nonce
        self._nonce += 1
        return n

    async def reconcile_onchain(self) -> dict:
        """崩溃恢复：本地 nonce 与链上对齐 + pending tx 归位。供 Reconciler 调用。"""
        client = self._ensure_client()
        chain_nonce = await client.get_nonce(getattr(self._signer, "address", "0x0"))
        discrepancies: list[str] = []
        if self._nonce is not None and self._nonce != chain_nonce:
            discrepancies.append(f"nonce 本地 {self._nonce} ≠ 链上 {chain_nonce}，已对齐")
        self._nonce = chain_nonce
        settled: list[str] = []
        for tx_hash, purpose in list(self._pending_txs.items()):
            receipt = await client.wait_receipt(tx_hash)
            if receipt is not None:
                settled.append(f"{purpose} {tx_hash} → status={receipt['status']}")
                self._pending_txs.pop(tx_hash, None)
        return {"nonce": chain_nonce, "discrepancies": discrepancies,
                "settled": settled, "pending": list(self._pending_txs)}

    # ---- 执行 ------------------------------------------------------------------

    async def preflight(self, intent: OrderIntent) -> PreflightReport:
        if self._client is None:
            return PreflightReport(ok=False, est_price=Decimal(0), reasons=["链上场所未配置"])
        q = await self._client.quote_swap(intent.symbol, intent.side, intent.size_quote)
        price = Decimal(str(q["price"]))
        impact = Decimal(str(q.get("price_impact", 0)))
        reasons: list[str] = []
        if price <= 0:
            reasons.append("DEX 无有效报价")
        return PreflightReport(ok=not reasons, est_price=price,
                               est_slippage_bps=impact * Decimal(10000),
                               est_price_impact=impact, reasons=reasons)

    async def quote_context(self, intent: OrderIntent) -> dict:
        """给风控引擎的链上上下文（授权/白名单/TVL/gas/MEV），组装 RiskContext 用。"""
        client = self._ensure_client()
        q = await client.quote_swap(intent.symbol, intent.side, intent.size_quote)
        router = q.get("router", "")
        allowance = await client.allowance(intent.symbol, router)
        need_approve = allowance < intent.size_quote
        return {
            "onchain": True,
            "approval_amount": intent.size_quote if need_approve else None,
            "contract_address": router,
            "pool_tvl_usd": Decimal(str(q.get("pool_tvl_usd", 0))),
            "est_gas_quote": Decimal(str(q.get("gas_quote", 0))),
            "mev_protected": bool(q.get("mev_protected", False)),
        }

    async def place(self, intent: OrderIntent) -> ExecutionResult:
        if intent.client_id in self._fills:          # 重放去重（崩溃重放不重复上链）
            return self._fills[intent.client_id]

        client = self._ensure_client()
        try:
            res = await self._place_inner(client, intent)
        except Exception as e:  # noqa: BLE001 —— RPC/链上异常不击穿上层
            res = ExecutionResult(client_id=intent.client_id, status="failed",
                                  chain=self.chain, error=f"链上执行异常：{e}")
        self._fills[intent.client_id] = res
        return res

    async def _place_inner(self, client, intent: OrderIntent) -> ExecutionResult:
        q = await client.quote_swap(intent.symbol, intent.side, intent.size_quote)
        price = Decimal(str(q["price"]))
        router = q.get("router", "")
        gas_total = Decimal(0)

        if intent.reduce_only or intent.side == "flat":
            return await self._close(client, intent, price)

        if intent.side != "long":
            return ExecutionResult(client_id=intent.client_id, status="rejected",
                                   chain=self.chain, error="链上 M3 仅支持现货买入/卖出（无杠杆做空）")

        # ① 精确额度 approve（仅在额度不足时）——禁无限授权
        allowance = await client.allowance(intent.symbol, router)
        if allowance < intent.size_quote:
            nonce = await self._next_nonce()
            tx = await client.send_approve(intent.symbol, router, intent.size_quote, nonce)
            self._pending_txs[tx] = "approve"
            receipt = await client.wait_receipt(tx)
            self._pending_txs.pop(tx, None)
            if not receipt or receipt["status"] != 1:
                return ExecutionResult(client_id=intent.client_id, status="failed",
                                       chain=self.chain, tx_hash=tx, error="approve 交易 revert")
            gas_total += Decimal(str(receipt.get("gas_used_quote", 0)))

        # ② swap（min_out 按报价 - 1% 容忍）
        size_base = intent.size_quote / price
        min_out = size_base * Decimal("0.99")
        nonce = await self._next_nonce()
        tx = await client.send_swap(intent.symbol, intent.side, intent.size_quote, min_out, nonce)
        self._pending_txs[tx] = "swap"
        receipt = await client.wait_receipt(tx)
        self._pending_txs.pop(tx, None)
        if not receipt or receipt["status"] != 1:
            return ExecutionResult(client_id=intent.client_id, status="failed",
                                   chain=self.chain, tx_hash=tx,
                                   error="swap 交易 revert（滑点超容忍或流动性变化）")
        gas_total += Decimal(str(receipt.get("gas_used_quote", 0)))

        self._free_quote -= intent.size_quote + gas_total
        self._positions[intent.symbol] = Position(
            symbol=intent.symbol, venue=self.id, side="long", instrument="spot",
            size_base=size_base, entry_price=price, chain=self.chain, tx_hash=tx,
        )
        self.log.info("onchain_filled", symbol=intent.symbol, tx=tx, gas=str(gas_total))
        return ExecutionResult(
            client_id=intent.client_id, order_id=tx, status="filled",
            filled_base=size_base, avg_price=price, fee_quote=gas_total,
            slippage_bps=Decimal(str(q.get("price_impact", 0))) * Decimal(10000),
            chain=self.chain, tx_hash=tx, gas_used=gas_total,
        )

    async def _close(self, client, intent: OrderIntent, price: Decimal) -> ExecutionResult:
        pos = self._positions.get(intent.symbol)
        if pos is None:
            return ExecutionResult(client_id=intent.client_id, status="rejected",
                                   chain=self.chain, error="无持仓可平")
        nonce = await self._next_nonce()
        tx = await client.send_swap(intent.symbol, "flat", pos.size_base * price,
                                    pos.size_base, nonce)
        self._pending_txs[tx] = "close"
        receipt = await client.wait_receipt(tx)
        self._pending_txs.pop(tx, None)
        if not receipt or receipt["status"] != 1:
            return ExecutionResult(client_id=intent.client_id, status="failed",
                                   chain=self.chain, tx_hash=tx, error="平仓 swap revert")
        gas = Decimal(str(receipt.get("gas_used_quote", 0)))
        self._positions.pop(intent.symbol, None)
        self._free_quote += pos.size_base * price - gas
        return ExecutionResult(
            client_id=intent.client_id, order_id=tx, status="filled",
            filled_base=pos.size_base, avg_price=price, fee_quote=gas,
            chain=self.chain, tx_hash=tx, gas_used=gas,
        )

    async def cancel(self, order_id: str) -> None:
        return None   # 链上交易无撤单；pending tx 处理走 reconcile_onchain

    async def close(self) -> None:
        close = getattr(self._client, "close", None)
        if close is not None:
            await close()
