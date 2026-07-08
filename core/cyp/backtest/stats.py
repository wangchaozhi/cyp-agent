"""反过拟合统计套件：PSR / Deflated Sharpe / 最小回测长度（纯 Python，零重依赖）。

治当前网格扫参的多重检验病：从 N 组里挑"最优"会系统性高估夏普。
- PSR（概率夏普，Bailey & López de Prado）：夏普显著大于基准的概率，含偏度/峰度校正。
- DSR（Deflated Sharpe）：把基准设为「N 次试验的期望最大夏普」，试验越多门槛越高。
- MinTRL：达到目标置信所需的最小回测长度。
参考：Bailey, López de Prado, "The Deflated Sharpe Ratio" (2014)。
"""

from __future__ import annotations

import math
from statistics import fmean, pvariance

_EULER = 0.5772156649015329


def norm_cdf(x: float) -> float:
    return 0.5 * (1.0 + math.erf(x / math.sqrt(2.0)))


def norm_ppf(p: float) -> float:
    """标准正态分位数（Acklam 有理逼近，精度足够）。"""
    if p <= 0.0:
        return -math.inf
    if p >= 1.0:
        return math.inf
    a = [-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02,
         1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00]
    b = [-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02,
         6.680131188771972e+01, -1.328068155288572e+01]
    c = [-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00,
         -2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00]
    d = [7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00, 3.754408661907416e+00]
    plow, phigh = 0.02425, 1 - 0.02425
    if p < plow:
        q = math.sqrt(-2 * math.log(p))
        return (((((c[0] * q + c[1]) * q + c[2]) * q + c[3]) * q + c[4]) * q + c[5]) / \
               ((((d[0] * q + d[1]) * q + d[2]) * q + d[3]) * q + 1)
    if p > phigh:
        q = math.sqrt(-2 * math.log(1 - p))
        return -(((((c[0] * q + c[1]) * q + c[2]) * q + c[3]) * q + c[4]) * q + c[5]) / \
               ((((d[0] * q + d[1]) * q + d[2]) * q + d[3]) * q + 1)
    q = p - 0.5
    r = q * q
    return (((((a[0] * r + a[1]) * r + a[2]) * r + a[3]) * r + a[4]) * r + a[5]) * q / \
           (((((b[0] * r + b[1]) * r + b[2]) * r + b[3]) * r + b[4]) * r + 1)


def _moments(returns: list[float]) -> tuple[float, float, float, float]:
    """返回 (夏普, 偏度, 峰度[非超额,正态=3], n)。"""
    n = len(returns)
    mu = fmean(returns)
    var = pvariance(returns)
    sd = math.sqrt(var)
    if sd == 0:
        return 0.0, 0.0, 3.0, n
    sr = mu / sd
    skew = fmean([((x - mu) / sd) ** 3 for x in returns])
    kurt = fmean([((x - mu) / sd) ** 4 for x in returns])
    return sr, skew, kurt, n


def sharpe(returns: list[float]) -> float:
    return _moments(returns)[0] if len(returns) > 1 else 0.0


def probabilistic_sharpe(returns: list[float], sr_benchmark: float = 0.0) -> float:
    """PSR：观测夏普显著大于 sr_benchmark 的概率（0..1）。"""
    if len(returns) < 2:
        return 0.0
    sr, skew, kurt, n = _moments(returns)
    denom = math.sqrt(max(1e-12, 1 - skew * sr + (kurt - 1) / 4 * sr * sr))
    z = (sr - sr_benchmark) * math.sqrt(n - 1) / denom
    return norm_cdf(z)


def expected_max_sharpe(trial_sharpes: list[float]) -> float:
    """N 次独立试验的期望最大夏普（用于 DSR 的基准）。"""
    n = len(trial_sharpes)
    if n < 2:
        return 0.0
    sd = math.sqrt(pvariance(trial_sharpes))
    if sd == 0:
        return 0.0
    z1 = norm_ppf(1 - 1.0 / n)
    z2 = norm_ppf(1 - 1.0 / (n * math.e))
    return sd * ((1 - _EULER) * z1 + _EULER * z2)


def deflated_sharpe(returns: list[float], trial_sharpes: list[float]) -> float:
    """DSR：把基准设为期望最大夏普 → 试验越多，达标越难（抑制过拟合）。"""
    return probabilistic_sharpe(returns, expected_max_sharpe(trial_sharpes))


def min_track_record_length(returns: list[float], sr_benchmark: float = 0.0,
                            target_prob: float = 0.95) -> float:
    """MinTRL：要以 target_prob 确信夏普>基准，所需的最小样本数。"""
    if len(returns) < 2:
        return math.inf
    sr, skew, kurt, _ = _moments(returns)
    if sr <= sr_benchmark:
        return math.inf
    denom = 1 - skew * sr + (kurt - 1) / 4 * sr * sr
    return 1 + denom * (norm_ppf(target_prob) / (sr - sr_benchmark)) ** 2
