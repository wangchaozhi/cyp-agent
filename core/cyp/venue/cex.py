"""CexVenue：ccxt 统一接入中心化交易所。M0 只做只读行情（无需密钥）。

实盘下单（M2）在此扩展：原生保护单、持仓/保证金模式、幂等 clientOrderId 等
「每家交易所必做的适配」（见 docs/ARCHITECTURE.md「CEX 适配点」）。
参考实现 = Binance。
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
from cyp.venue.base import PreflightReport, VenueCaps


class CexVenue:
    kind = "cex"

    def __init__(self, exchange_id: str = "binance", api_key: str | None = None,
                 api_secret: str | None = None, read_only: bool = True) -> None:
        self.id = exchange_id
        self._api_key = api_key
        self._api_secret = api_secret
        self.caps = VenueCaps(spot=True, perp=True, native_protective_orders=True, read_only=read_only)
        self._client = None  # 惰性初始化 ccxt

    def is_configured(self) -> bool:
        # 只读行情始终可用；下单需密钥
        return True if self.caps.read_only else bool(self._api_key and self._api_secret)

    def _ccxt(self):
        if self._client is None:
            try:
                import ccxt.async_support as ccxt  # 惰性导入，未装 ccxt 时给出清晰提示
            except ImportError as e:  # pragma: no cover
                raise RuntimeError("需要 ccxt：pip install ccxt") from e
            cls = getattr(ccxt, self.id)
            self._client = cls({
                "apiKey": self._api_key,
                "secret": self._api_secret,
                "enableRateLimit": True,
            })
        return self._client

    async def close(self) -> None:
        if self._client is not None:
            await self._client.close()

    # ---- 行情（只读）------------------------------------------------------

    async def fetch_ticker(self, symbol: str) -> Decimal:
        t = await self._ccxt().fetch_ticker(symbol)
        return Decimal(str(t["last"]))

    async def fetch_ohlcv(self, symbol: str, timeframe: str = "1h", limit: int = 200) -> list[Candle]:
        rows = await self._ccxt().fetch_ohlcv(symbol, timeframe=timeframe, limit=limit)
        out: list[Candle] = []
        from datetime import datetime, timezone
        for ts, o, h, l, c, v in rows:
            out.append(Candle(
                ts=datetime.fromtimestamp(ts / 1000, tz=timezone.utc),
                open=Decimal(str(o)), high=Decimal(str(h)), low=Decimal(str(l)),
                close=Decimal(str(c)), volume=Decimal(str(v)),
            ))
        return out

    async def fetch_orderbook(self, symbol: str, depth: int = 20) -> OrderBook:
        ob = await self._ccxt().fetch_order_book(symbol, limit=depth)
        to_pairs = lambda rows: [(Decimal(str(p)), Decimal(str(s))) for p, s in rows]
        return OrderBook(bids=to_pairs(ob["bids"]), asks=to_pairs(ob["asks"]))

    # ---- 账户/执行（M2 实现）--------------------------------------------

    async def positions(self) -> list[Position]:
        return []  # M0 只读，无持仓查询

    async def balances(self) -> Balances:
        return Balances()

    async def preflight(self, intent: OrderIntent) -> PreflightReport:
        raise NotImplementedError("CexVenue 实盘执行在 M2；M0 请用 PaperVenue")

    async def place(self, intent: OrderIntent) -> ExecutionResult:
        raise NotImplementedError("CexVenue 实盘执行在 M2；M0 请用 PaperVenue")

    async def cancel(self, order_id: str) -> None:
        raise NotImplementedError("CexVenue 实盘执行在 M2")
