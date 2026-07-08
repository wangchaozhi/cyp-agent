"""反过拟合统计：正态 CDF/PPF、PSR、Deflated Sharpe、MinTRL。纯 Python。"""

import math

from cyp.backtest.stats import (
    deflated_sharpe,
    expected_max_sharpe,
    min_track_record_length,
    norm_cdf,
    norm_ppf,
    probabilistic_sharpe,
    sharpe,
)


def test_norm_cdf_known_points():
    assert abs(norm_cdf(0) - 0.5) < 1e-9
    assert abs(norm_cdf(1.96) - 0.975) < 1e-3


def test_norm_ppf_inverse_of_cdf():
    for x in (-2.0, -0.5, 0.3, 1.5):
        assert abs(norm_ppf(norm_cdf(x)) - x) < 1e-4


def test_sharpe_sign():
    up = [0.01, 0.02, 0.015, 0.01, 0.02]
    assert sharpe(up) > 0
    assert sharpe([-x for x in up]) < 0


def test_psr_in_unit_interval_and_monotone_in_n():
    short = [0.01, -0.005, 0.012, 0.008]
    long = short * 10                      # 更多样本 → 同分布下 PSR 更高（更确信）
    p_short = probabilistic_sharpe(short)
    p_long = probabilistic_sharpe(long)
    assert 0.0 <= p_short <= 1.0 and 0.0 <= p_long <= 1.0
    assert p_long > p_short


def test_expected_max_sharpe_grows_with_trials():
    trials_small = [0.1, 0.2, 0.15]
    trials_big = [0.1, 0.2, 0.15, 0.05, 0.25, 0.18, 0.3, 0.02]
    assert expected_max_sharpe(trials_big) > expected_max_sharpe(trials_small)


def test_deflated_sharpe_penalizes_multiple_trials():
    returns = [0.01, -0.004, 0.013, 0.009, 0.011, -0.002, 0.014, 0.007]
    raw_psr = probabilistic_sharpe(returns, 0.0)
    trial_srs = [sharpe(returns)] + [0.05, 0.1, 0.08, 0.12, 0.03, 0.09]
    dsr = deflated_sharpe(returns, trial_srs)
    assert dsr < raw_psr                   # 扣除"挑了多组"的运气 → 更保守


def test_min_trl_infinite_when_no_edge():
    losing = [-0.01, -0.02, -0.005]
    assert min_track_record_length(losing) == math.inf
