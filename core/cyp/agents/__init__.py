"""多智能体：分析师团 → 首席策略官 → 风控官 → 交易员 → 复盘官。

每个 Agent 显式注入依赖、必带规则降级路径。协作规格见 docs/AGENTS.md。
"""

from cyp.agents.analysts import (
    ANALYSTS,
    DerivativesAnalyst,
    OnchainAnalyst,
    SentimentAnalyst,
    TechnicalAnalyst,
)
from cyp.agents.base import AgentContext, Vote, blend, stance_sign
from cyp.agents.reviewer import Reviewer
from cyp.agents.risk_officer import RiskOfficer
from cyp.agents.strategist import Strategist
from cyp.agents.trader import Trader

__all__ = [
    "AgentContext",
    "Vote",
    "blend",
    "stance_sign",
    "ANALYSTS",
    "TechnicalAnalyst",
    "DerivativesAnalyst",
    "SentimentAnalyst",
    "OnchainAnalyst",
    "Strategist",
    "RiskOfficer",
    "Trader",
    "Reviewer",
]
