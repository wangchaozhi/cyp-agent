"""尾部风险度量：Historical VaR / CVaR / 计价币金额。"""

from decimal import Decimal

import pytest
from cyp.risk import (
    conditional_value_at_risk,
    historical_var,
    losses_from_returns,
    tail_risk_quote,
)
from cyp.risk.measures import quantile


def test_losses_from_returns_are_non_negative():
    assert losses_from_returns([0.02, 0.0, -0.03]) == [0.0, 0.0, 0.03]


def test_quantile_linear_interpolation():
    assert quantile([0, 10], 0.25) == 2.5
    assert quantile([0, 10], 0.5) == 5


def test_historical_var_known_values():
    returns = [0.01, -0.01, -0.02, -0.05, -0.10]
    # losses = [0, .01, .02, .05, .10]；80% 分位位于 .05 与 .10 之间
    assert historical_var(returns, 0.8) == pytest.approx(0.06)


def test_cvar_is_tail_average_and_not_below_var():
    returns = [0.01, -0.01, -0.02, -0.05, -0.10]
    var = historical_var(returns, 0.8)
    cvar = conditional_value_at_risk(returns, 0.8)
    assert cvar == pytest.approx(0.10)
    assert cvar >= var


def test_tail_risk_quote_marks_small_samples_degraded():
    tr = tail_risk_quote([0.01, -0.05, -0.10], Decimal("10000"), confidence=0.8, min_samples=10)
    assert tr.degraded
    assert tr.n == 3
    assert tr.var_quote > 0
    assert tr.cvar_quote >= tr.var_quote


def test_confidence_bounds():
    with pytest.raises(ValueError):
        historical_var([0.01], 1.0)
