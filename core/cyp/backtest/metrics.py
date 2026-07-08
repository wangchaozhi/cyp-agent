"""回测绩效指标：纯函数，便于单测。

- total_return：期末/期初 - 1
- max_drawdown：净值曲线峰谷最大回撤
- sharpe：逐 bar 收益均值/标准差（未年化，跨回测可比即可）
- win_rate / profit_factor：按已平仓交易
"""

from __future__ import annotations

from statistics import fmean, pstdev


def compute_metrics(initial_equity: float, equity_curve: list[float], trades: list[dict]) -> dict:
    final = equity_curve[-1] if equity_curve else initial_equity
    total_return = (final - initial_equity) / initial_equity if initial_equity else 0.0

    # 最大回撤
    peak, max_dd = (equity_curve[0] if equity_curve else initial_equity), 0.0
    for eq in equity_curve:
        peak = max(peak, eq)
        if peak > 0:
            max_dd = max(max_dd, (peak - eq) / peak)

    # 夏普（逐 bar 收益）
    rets = [equity_curve[i] / equity_curve[i - 1] - 1
            for i in range(1, len(equity_curve)) if equity_curve[i - 1] > 0]
    sd = pstdev(rets) if len(rets) > 1 else 0.0
    sharpe = (fmean(rets) / sd) if sd > 0 else 0.0

    pnls = [t["pnl"] for t in trades]
    wins = [p for p in pnls if p > 0]
    losses = [p for p in pnls if p < 0]
    win_rate = (len(wins) / len(pnls)) if pnls else 0.0
    gross_win, gross_loss = sum(wins), -sum(losses)
    profit_factor = (gross_win / gross_loss) if gross_loss > 0 else (float("inf") if gross_win > 0 else 0.0)

    return {
        "initial_equity": round(initial_equity, 2),
        "final_equity": round(final, 2),
        "total_return": round(total_return, 4),
        "max_drawdown": round(max_dd, 4),
        "sharpe": round(sharpe, 4),
        "n_trades": len(pnls),
        "win_rate": round(win_rate, 4),
        "profit_factor": (round(profit_factor, 4) if profit_factor != float("inf") else None),
    }
