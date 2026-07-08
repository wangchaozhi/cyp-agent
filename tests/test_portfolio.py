"""组合账本：回撤/连亏/下单频率 + 接入编排器让熔断真正触发。离线。"""

import asyncio
from datetime import datetime, timedelta, timezone
from decimal import Decimal

from cyp.approval import AutoApprove
from cyp.config import Settings
from cyp.contracts import Candle, DerivativesData, MarketSnapshot, SentimentData
from cyp.orchestrator import Orchestrator
from cyp.portfolio import PortfolioTracker
from cyp.venue import PaperVenue

run = asyncio.run
NOW = datetime(2026, 7, 8, tzinfo=timezone.utc)


def test_total_drawdown_from_hwm():
    t = PortfolioTracker()
    t.update_equity(Decimal("10000"))          # 高水位
    t.update_equity(Decimal("9000"))           # 回落（hwm 不降）
    assert t.total_drawdown(Decimal("9000")) == Decimal("0.1")
    assert t.total_drawdown(Decimal("10500")) == Decimal("0")   # 新高无回撤


def test_orders_last_hour_window():
    t = PortfolioTracker()
    t.record_order(NOW - timedelta(minutes=90))   # 窗口外
    t.record_order(NOW - timedelta(minutes=30))   # 窗口内
    t.record_order(NOW - timedelta(minutes=5))    # 窗口内
    assert t.orders_last_hour(NOW) == 2


def test_consecutive_losses_and_daily_drawdown():
    t = PortfolioTracker()
    t.update_equity(Decimal("10000"))
    t.record_close(Decimal("-100"), NOW)
    t.record_close(Decimal("-200"), NOW)
    assert t.consecutive_losses == 2
    assert t.daily_drawdown(NOW) == Decimal("300") / Decimal("10000")
    t.record_close(Decimal("50"), NOW)            # 一笔盈利
    assert t.consecutive_losses == 0              # 连亏清零


def test_risk_snapshot_shape():
    t = PortfolioTracker()
    t.update_equity(Decimal("10000"))
    snap = t.risk_snapshot(Decimal("9500"), NOW)
    assert set(snap) == {"orders_last_hour", "consecutive_losses",
                         "daily_drawdown", "weekly_drawdown", "total_drawdown"}
    assert snap["total_drawdown"] == Decimal("0.05")


class UptrendData:
    async def snapshot(self, symbol):
        candles = [Candle(ts=NOW, open=Decimal(50000 + i * 125), high=Decimal(50000 + i * 125 + 50),
                          low=Decimal(50000 + i * 125 - 50), close=Decimal(50000 + i * 125),
                          volume=Decimal("100")) for i in range(80)]
        return MarketSnapshot(symbol=symbol, venue="synthetic", ohlcv=candles,
                              derivatives=DerivativesData(funding_rate=Decimal("-0.0005"),
                                                          long_short_ratio=Decimal("0.8")),
                              sentiment=SentimentData(fear_greed=20))


def test_order_rate_breaker_fires_after_wiring():
    # 把频率上限压到 1：第一轮成交后，第二轮应被 order_rate 否决（证明账本已接入风控）
    settings = Settings(_env_file=None)
    settings.risk.max_orders_per_hour = 1
    orch = Orchestrator(settings, UptrendData(), PaperVenue(), approval=AutoApprove())
    r1 = run(orch.run_once("BTC/USDT"))
    assert r1.status == "executed"
    assert orch.portfolio.orders_last_hour() == 1
    r2 = run(orch.run_once("BTC/USDT"))
    assert r2.status == "rejected"
    assert any("order_rate" in v for v in r2.assessment.hard_violations)
