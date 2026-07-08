"""扫参 CLI：对合成历史批量回测不同策略配置，按目标函数排序打印。

    python -m cyp.backtest.sweep --bars 300 --top 5
"""

from __future__ import annotations

import argparse
import asyncio
import contextlib
import sys
from decimal import Decimal

from cyp.backtest import grid, robust_sweep, sweep
from cyp.config import Settings
from cyp.data import SyntheticMarketData


def main(argv: list[str] | None = None) -> int:
    for stream in (sys.stdout, sys.stderr):
        with contextlib.suppress(Exception):
            stream.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]

    parser = argparse.ArgumentParser(prog="cyp-sweep", description="cyp-agent 策略扫参择优（合成历史）")
    parser.add_argument("--symbol", default="BTC/USDT")
    parser.add_argument("--bars", type=int, default=300)
    parser.add_argument("--window", type=int, default=60)
    parser.add_argument("--seed", type=int, default=7)
    parser.add_argument("--drift", type=float, default=0.001)
    parser.add_argument("--top", type=int, default=5)
    args = parser.parse_args(argv)

    settings = Settings()
    candles = asyncio.run(
        SyntheticMarketData(bars=args.bars, seed=args.seed, drift=args.drift).snapshot(args.symbol)).ohlcv

    configs = grid(
        enter_threshold=[0.08, 0.12, 0.18],
        k_stop=[Decimal("1.5"), Decimal("2"), Decimal("3")],
        k_tp=[Decimal("2"), Decimal("3"), Decimal("4")],
    )
    results = asyncio.run(sweep(settings, args.symbol, candles, configs, window=args.window))

    print(f"扫参 {args.symbol} · {args.bars} bars · {len(configs)} 组配置 · 目标=收益-回撤")
    print("-" * 74)
    print(f"{'score':>8} {'收益%':>8} {'回撤%':>7} {'夏普':>7} {'交易':>4} {'胜率%':>6}  参数（样本内，仅参考）")
    for r in results[:args.top]:
        m, c = r.metrics, r.config
        print(f"{r.score:>8.4f} {m['total_return']*100:>7.2f} {m['max_drawdown']*100:>6.2f} "
              f"{m['sharpe']:>7.3f} {m['n_trades']:>4} {m['win_rate']*100:>5.1f}  "
              f"enter={c.enter_threshold} kSL={c.k_stop} kTP={c.k_tp}")

    # 诚实择优：样本内选 → 样本外验证 + 过拟合检验
    rr = asyncio.run(robust_sweep(settings, args.symbol, candles, configs, window=args.window))
    print("=" * 74)
    print("诚实择优（样本内挑最优 → 样本外验证，防过拟合）")
    print(f"  样本内 收益 {rr.is_metrics['total_return']*100:+.2f}%   "
          f"样本外 收益 {rr.oos_metrics['total_return']*100:+.2f}%  回撤 {rr.oos_metrics['max_drawdown']*100:.2f}%")
    print(f"  PBO(过拟合概率) {rr.pbo}   Deflated Sharpe {rr.deflated_sharpe}")
    b = rr.best_config
    print(f"  裁决：{rr.verdict}   参数 enter={b.enter_threshold} kSL={b.k_stop} kTP={b.k_tp}")
    if "REJECT" in rr.verdict:
        print("  → 样本内最优在样本外未通过：多半是噪声，勿上实盘。")
    print("=" * 74)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
