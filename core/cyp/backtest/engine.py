"""Backtester：用同一套 分析→决策→风控 管线跑历史，加止损/止盈退出管理。

「回测/模拟/实盘三档统一」：入场决策复用 Orchestrator（AutoApprove + PaperVenue），
仅在回测层补上按 bar 高低价触发的止损/止盈平仓，实现完整 round-trip 与绩效归因。
M5 v1：单持仓（一次一仓），一个标的。
"""

from __future__ import annotations

from decimal import Decimal

from pydantic import BaseModel

from cyp.agents import StrategyConfig
from cyp.approval import AutoApprove
from cyp.backtest.data import HistoricalData
from cyp.backtest.metrics import compute_metrics
from cyp.config import Settings
from cyp.contracts import Candle, OrderIntent
from cyp.orchestrator import Orchestrator
from cyp.venue import PaperVenue


class BacktestReport(BaseModel):
    symbol: str
    n_bars: int
    metrics: dict
    trades: list[dict]
    equity_curve: list[float]


class Backtester:
    def __init__(self, settings: Settings, symbol: str, candles: list[Candle],
                 window: int = 60, initial_quote: Decimal = Decimal("10000"),
                 strategy: StrategyConfig | None = None) -> None:
        self.settings = settings
        self.symbol = symbol
        self.candles = candles
        self.window = window
        self.initial = float(initial_quote)
        self.venue = PaperVenue(initial_quote=initial_quote)
        self.data = HistoricalData(symbol, candles, window)
        self.orch = Orchestrator(settings, self.data, self.venue,
                                 approval=AutoApprove(), strategy=strategy)
        self.equity_curve: list[float] = []
        self.trades: list[dict] = []
        self.active: dict | None = None

    async def run(self) -> BacktestReport:
        for i in range(self.window, len(self.candles)):
            bar = self.candles[i]
            self.data.set_cursor(i)
            self.venue.set_mark_price(self.symbol, bar.close)

            if self.active:                         # 先按当前 bar 高低价检查止损/止盈
                ex = self._exit_price(bar)
                if ex is not None:
                    await self._close(ex, i)

            if not self.active:                     # 空仓才找新机会（单持仓）
                res = await self.orch.run_once(self.symbol, run_id=f"bt{i}")
                if res.status == "executed" and res.proposal.side in ("long", "short"):
                    self.active = {
                        "side": res.proposal.side, "instrument": res.proposal.instrument,
                        "entry": res.execution.avg_price, "size_base": res.execution.filled_base,
                        "stop": res.proposal.stop_loss,
                        "tp": res.proposal.take_profit[0] if res.proposal.take_profit else None,
                        "bar_in": i,
                    }

            self.equity_curve.append(float((await self.venue.balances()).total_quote))

        if self.active:                             # 收尾：末价平掉未平仓
            await self._close(self.candles[-1].close, len(self.candles) - 1)
            self.equity_curve.append(float((await self.venue.balances()).total_quote))

        return BacktestReport(
            symbol=self.symbol, n_bars=len(self.candles), trades=self.trades,
            equity_curve=self.equity_curve,
            metrics=compute_metrics(self.initial, self.equity_curve, self.trades),
        )

    def _exit_price(self, bar: Candle) -> Decimal | None:
        a = self.active
        if a["side"] == "long":
            if a["stop"] is not None and bar.low <= a["stop"]:
                return a["stop"]
            if a["tp"] is not None and bar.high >= a["tp"]:
                return a["tp"]
        else:
            if a["stop"] is not None and bar.high >= a["stop"]:
                return a["stop"]
            if a["tp"] is not None and bar.low <= a["tp"]:
                return a["tp"]
        return None

    async def _close(self, price: Decimal, i: int) -> None:
        a = self.active
        self.venue.set_mark_price(self.symbol, price)
        res = await self.venue.place(OrderIntent(
            client_id=f"bt-close-{i}", symbol=self.symbol, venue="paper", side=a["side"],
            instrument=a["instrument"], reduce_only=True, size_quote=Decimal(0)))
        fill = res.avg_price if res.avg_price is not None else price
        entry, size = a["entry"], a["size_base"]
        pnl = float((fill - entry) * size) if a["side"] == "long" else float((entry - fill) * size)
        self.orch.portfolio.record_close(Decimal(str(pnl)))
        self.trades.append({"side": a["side"], "entry": float(entry), "exit": float(fill),
                            "pnl": round(pnl, 2), "bar_in": a["bar_in"], "bar_out": i})
        self.active = None
