"""确定性风控引擎（★非 LLM，一票否决）。

- 纯函数集合：不调 LLM、不发网络请求，输入齐全即可判定。
- 拥有对任何 TradeProposal 的否决权；LLM 风控官只能在其之上收紧，不能放宽。
- 每条规则一个纯函数、一个单测（含通过/否决/缩仓边界）。详见 docs/RISK.md。
"""

from cyp.risk.engine import assess
from cyp.risk.measures import (
    TailRisk,
    conditional_value_at_risk,
    historical_var,
    losses_from_returns,
    tail_risk_quote,
)
from cyp.risk.rules import RiskContext, RuleAction, RuleResult

__all__ = [
    "assess",
    "RiskContext",
    "RuleAction",
    "RuleResult",
    "TailRisk",
    "losses_from_returns",
    "historical_var",
    "conditional_value_at_risk",
    "tail_risk_quote",
]
