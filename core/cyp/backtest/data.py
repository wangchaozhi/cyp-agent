"""历史回放行情源：按游标返回"截至当前 bar"的窗口快照，喂给同一套分析管线。"""

from __future__ import annotations

from cyp.contracts import Candle, MarketSnapshot


class HistoricalData:
    def __init__(self, symbol: str, candles: list[Candle], window: int = 60) -> None:
        self.symbol = symbol
        self.candles = candles
        self.window = window
        self._cursor = window

    def set_cursor(self, i: int) -> None:
        self._cursor = i

    async def snapshot(self, symbol: str) -> MarketSnapshot:
        lo = max(0, self._cursor - self.window)
        window = self.candles[lo:self._cursor + 1]
        # 历史衍生品/情绪通常不可得 → None（对应分析师降级，技术面主导），与实盘降级一致
        return MarketSnapshot(symbol=symbol, venue="backtest", ohlcv=window)
