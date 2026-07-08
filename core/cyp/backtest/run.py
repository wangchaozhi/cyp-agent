"""回测 CLI：合成历史行情跑一遍回测并打印绩效。

    python -m cyp.backtest.run --symbol BTC/USDT --bars 300 --drift 0.001

零密钥离线：用确定性合成行情验证「同一套管线跑历史」。接真实历史数据在后续迭代。
"""

from __future__ import annotations

import argparse
import asyncio
import contextlib
import sys

from cyp.backtest import Backtester
from cyp.config import Settings
from cyp.data import SyntheticMarketData


def main(argv: list[str] | None = None) -> int:
    for stream in (sys.stdout, sys.stderr):
        with contextlib.suppress(Exception):
            stream.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]

    parser = argparse.ArgumentParser(prog="cyp-backtest", description="cyp-agent 回测（合成历史）")
    parser.add_argument("--symbol", default="BTC/USDT")
    parser.add_argument("--bars", type=int, default=300)
    parser.add_argument("--window", type=int, default=60)
    parser.add_argument("--seed", type=int, default=7)
    parser.add_argument("--drift", type=float, default=0.001)
    args = parser.parse_args(argv)

    settings = Settings()
    candles = asyncio.run(
        SyntheticMarketData(bars=args.bars, seed=args.seed, drift=args.drift).snapshot(args.symbol)).ohlcv
    report = asyncio.run(Backtester(settings, args.symbol, candles, window=args.window).run())

    m = report.metrics
    print(f"回测 {report.symbol} · {report.n_bars} bars · window={args.window} · drift={args.drift}")
    print("-" * 52)
    print(f"  期初净值   {m['initial_equity']}")
    print(f"  期末净值   {m['final_equity']}")
    print(f"  总收益     {m['total_return'] * 100:+.2f}%")
    print(f"  最大回撤   {m['max_drawdown'] * 100:.2f}%")
    print(f"  夏普       {m['sharpe']}")
    print(f"  交易数     {m['n_trades']}   胜率 {m['win_rate'] * 100:.1f}%   盈亏比 {m['profit_factor']}")
    print("-" * 52)
    for t in report.trades[:10]:
        print(f"  [{t['bar_in']}→{t['bar_out']}] {t['side']:5s} {t['entry']:.2f}→{t['exit']:.2f}  盈亏 {t['pnl']:+.2f}")
    if len(report.trades) > 10:
        print(f"  … 共 {len(report.trades)} 笔")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
