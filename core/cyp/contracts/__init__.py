"""契约单一来源：跨层流动的全部数据结构（pydantic v2）。

7 步闭环的数据流：
    MarketSnapshot → AnalystReport[] → TradeProposal → RiskAssessment
        → ApprovalDecision → OrderIntent → ExecutionResult → TradeReview

前端 TS 类型由这些模型生成（pydantic → JSON Schema → 类型），仪表盘只 import 生成物。
"""

from cyp.contracts.models import (
    AgentId,
    AnalystReport,
    ApprovalDecision,
    Balances,
    Candle,
    DerivativesData,
    ExecutionResult,
    MarketSnapshot,
    OnchainData,
    OrderBook,
    OrderIntent,
    Position,
    PricePlan,
    ProtectiveOrder,
    RiskAssessment,
    SentimentData,
    Signal,
    TradeProposal,
    TradeReview,
    Verdict,
)

__all__ = [
    "AgentId",
    "AnalystReport",
    "ApprovalDecision",
    "Balances",
    "Candle",
    "DerivativesData",
    "ExecutionResult",
    "MarketSnapshot",
    "OnchainData",
    "OrderBook",
    "OrderIntent",
    "Position",
    "PricePlan",
    "ProtectiveOrder",
    "RiskAssessment",
    "SentimentData",
    "Signal",
    "TradeProposal",
    "TradeReview",
    "Verdict",
]
