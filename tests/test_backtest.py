"""回测：绩效指标(纯) + 退出逻辑 + 端到端历史回放。离线确定性。"""

import asyncio
from datetime import datetime, timezone
from decimal import Decimal

from cyp.backtest import Backtester, compute_metrics
from cyp.config import Settings
from cyp.contracts import Candle
from cyp.data import SyntheticMarketData

run = asyncio.run


# ---- 绩效指标（纯函数）-----------------------------------------------------

def test_compute_metrics_known_values():
    m = compute_metrics(100.0, [100, 110, 105, 120], [{"pnl": 10}, {"pnl": -5}])
    assert m["total_return"] == 0.2
    assert m["max_drawdown"] == 0.0455          # (110-105)/110
    assert m["n_trades"] == 2 and m["win_rate"] == 0.5
    assert m["profit_factor"] == 2.0


def test_compute_metrics_all_wins_profit_factor_none():
    m = compute_metrics(100.0, [100, 130], [{"pnl": 30}])
    assert m["profit_factor"] is None           # 无亏损 → inf → None
    assert m["win_rate"] == 1.0


def test_compute_metrics_empty():
    m = compute_metrics(100.0, [], [])
    assert m["n_trades"] == 0 and m["total_return"] == 0.0


# ---- 退出逻辑 --------------------------------------------------------------

def _bt():
    candles = [Candle(ts=datetime.now(timezone.utc), open=Decimal("100"), high=Decimal("100"),
                      low=Decimal("100"), close=Decimal("100"), volume=Decimal("1")) for _ in range(5)]
    return Backtester(Settings(_env_file=None), "BTC/USDT", candles, window=2)


def _bar(high, low):
    return Candle(ts=datetime.now(timezone.utc), open=Decimal("100"), high=Decimal(str(high)),
                  low=Decimal(str(low)), close=Decimal("100"), volume=Decimal("1"))


def test_exit_long_hits_stop_then_tp():
    bt = _bt()
    bt.active = {"side": "long", "stop": Decimal("95"), "tp": Decimal("110")}
    assert bt._exit_price(_bar(105, 94)) == Decimal("95")     # 触止损
    assert bt._exit_price(_bar(111, 99)) == Decimal("110")    # 触止盈
    assert bt._exit_price(_bar(105, 99)) is None              # 都没碰


def test_exit_short_hits_stop_then_tp():
    bt = _bt()
    bt.active = {"side": "short", "stop": Decimal("105"), "tp": Decimal("90")}
    assert bt._exit_price(_bar(106, 100)) == Decimal("105")   # 空头止损在上
    assert bt._exit_price(_bar(101, 89)) == Decimal("90")     # 空头止盈在下


def test_close_realizes_pnl_and_clears_active():
    bt = _bt()
    bt.venue.set_mark_price("BTC/USDT", Decimal("100"))
    bt.active = {"side": "long", "instrument": "spot", "entry": Decimal("100"),
                 "size_base": Decimal("1"), "bar_in": 0}
    run(bt._close(Decimal("110"), 3))
    assert bt.active is None
    assert len(bt.trades) == 1 and bt.trades[0]["pnl"] > 0     # 100→110 盈利


# ---- 端到端回放 ------------------------------------------------------------

def test_backtest_runs_over_synthetic_history():
    candles = run(SyntheticMarketData(bars=150).snapshot("BTC/USDT")).ohlcv
    report = run(Backtester(Settings(_env_file=None), "BTC/USDT", candles, window=60).run())
    assert report.n_bars == 150
    assert len(report.equity_curve) >= 150 - 60
    assert set(report.metrics) >= {"total_return", "max_drawdown", "sharpe", "n_trades", "win_rate"}
    assert report.metrics["n_trades"] == len(report.trades)
