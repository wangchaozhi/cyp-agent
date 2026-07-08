"""技术指标：纯 Python 实现，无重型依赖，便于离线单测。

价格/资金用 Decimal，但指标计算内部用 float（TA 场景足够且更快）。
数据不足时返回 None，让上层显式处理（分析师据此降级）。
"""

from __future__ import annotations

from statistics import fmean, pstdev

from cyp.contracts import Candle


def _closes(candles: list[Candle]) -> list[float]:
    return [float(c.close) for c in candles]


def sma(values: list[float], n: int) -> float | None:
    if len(values) < n:
        return None
    return fmean(values[-n:])


def ema(values: list[float], n: int) -> float | None:
    if len(values) < n:
        return None
    k = 2 / (n + 1)
    e = fmean(values[:n])  # 用前 n 个均值做种子
    for v in values[n:]:
        e = v * k + e * (1 - k)
    return e


def rsi(values: list[float], n: int = 14) -> float | None:
    if len(values) < n + 1:
        return None
    gains, losses = 0.0, 0.0
    for i in range(-n, 0):
        diff = values[i] - values[i - 1]
        gains += max(diff, 0.0)
        losses += max(-diff, 0.0)
    avg_gain, avg_loss = gains / n, losses / n
    if avg_loss == 0:
        return 100.0
    rs = avg_gain / avg_loss
    return 100 - 100 / (1 + rs)


def macd(values: list[float], fast: int = 12, slow: int = 26, signal: int = 9) -> tuple[float, float] | None:
    """返回 (macd_line, signal_line)；数据不足返回 None。"""
    if len(values) < slow + signal:
        return None
    macd_series: list[float] = []
    for i in range(slow, len(values) + 1):
        window = values[:i]
        ef, es = ema(window, fast), ema(window, slow)
        if ef is None or es is None:
            continue
        macd_series.append(ef - es)
    if len(macd_series) < signal:
        return None
    sig = ema(macd_series, signal)
    if sig is None:
        return None
    return macd_series[-1], sig


def atr(candles: list[Candle], n: int = 14) -> float | None:
    if len(candles) < n + 1:
        return None
    trs: list[float] = []
    for i in range(1, len(candles)):
        hi, lo = float(candles[i].high), float(candles[i].low)
        pc = float(candles[i - 1].close)
        trs.append(max(hi - lo, abs(hi - pc), abs(lo - pc)))
    return fmean(trs[-n:])


def bollinger(values: list[float], n: int = 20, k: float = 2.0) -> tuple[float, float, float] | None:
    """返回 (lower, mid, upper)。"""
    if len(values) < n:
        return None
    window = values[-n:]
    mid = fmean(window)
    sd = pstdev(window)
    return mid - k * sd, mid, mid + k * sd


def indicator_snapshot(candles: list[Candle]) -> dict[str, float | None]:
    """一次算出技术面分析师所需的最新指标值。"""
    vals = _closes(candles)
    m = macd(vals)
    bb = bollinger(vals)
    return {
        "last_close": vals[-1] if vals else None,
        "sma_fast": sma(vals, 20),
        "sma_slow": sma(vals, 50),
        "ema_fast": ema(vals, 12),
        "rsi": rsi(vals, 14),
        "macd": m[0] if m else None,
        "macd_signal": m[1] if m else None,
        "atr": atr(candles, 14),
        "bb_lower": bb[0] if bb else None,
        "bb_upper": bb[2] if bb else None,
    }
