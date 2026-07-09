"""回测 CLI：合成或真实历史行情跑一遍回测并打印绩效。

    python -m cyp.backtest.run --symbol BTC/USDT --bars 300 --drift 0.001
    python -m cyp.backtest.run --data cex --exchange okx --timeframe 1h --bars 500

--data cex 时从交易所拉取真实历史 K 线（公共行情无需 Key），增量缓存到 PostgreSQL
（TimescaleDB hypertable，见 docker-compose.yml；DSN 取 CYP_DB_URL）。
"""

from __future__ import annotations

import argparse
import asyncio
import contextlib
import sys

from cyp.backtest import Backtester
from cyp.config import Settings
from cyp.data import SyntheticMarketData


async def load_real_candles(exchange_id: str, symbol: str, timeframe: str, bars: int,
                            dsn: str | None = None):
    """从交易所归档拉真实历史（只读，无需 Key）。"""
    from cyp.backtest import OhlcvArchive
    from cyp.venue import CexVenue
    venue = CexVenue(exchange_id=exchange_id, read_only=True)
    try:
        return await OhlcvArchive(dsn).ensure(venue, symbol, timeframe, bars)
    finally:
        await venue.close()


def main(argv: list[str] | None = None) -> int:
    for stream in (sys.stdout, sys.stderr):
        with contextlib.suppress(Exception):
            stream.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]

    parser = argparse.ArgumentParser(prog="cyp-backtest", description="cyp-agent 回测（合成/真实历史）")
    parser.add_argument("--symbol", default="BTC/USDT")
    parser.add_argument("--bars", type=int, default=300)
    parser.add_argument("--window", type=int, default=60)
    parser.add_argument("--seed", type=int, default=7)
    parser.add_argument("--drift", type=float, default=0.001)
    parser.add_argument("--data", choices=["synthetic", "cex"], default="synthetic")
    parser.add_argument("--exchange", default=None, help="--data cex 时的交易所（默认 CYP_CEX_ID）")
    parser.add_argument("--timeframe", default="1h")
    args = parser.parse_args(argv)

    settings = Settings()
    if args.data == "cex":
        exchange = args.exchange or settings.cex_id
        candles = asyncio.run(load_real_candles(exchange, args.symbol, args.timeframe, args.bars,
                                                dsn=settings.db_url))
        if len(candles) <= args.window:
            print(f"真实历史不足：拉到 {len(candles)} 根（需要 > window={args.window}）")
            return 1
        print(f"真实历史 {exchange} · {args.symbol} · {args.timeframe} · 共 {len(candles)} 根")
    else:
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
