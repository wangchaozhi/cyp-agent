"""契约模型定义。

约定：
- 金额/价格/数量一律用 Decimal，避免浮点误差（尤其涉及资金）。
- 所有跨 Agent 边界的数据都在此定义；Agent 内部临时结构不放这里。
- 字段尽量带 ge/le 约束，让非法值在边界即被拒（例如 confidence ∈ [0,1]）。
"""

from __future__ import annotations

from datetime import datetime, timezone
from decimal import Decimal
from typing import Literal

from pydantic import BaseModel, Field

# ---- 枚举/字面量 ------------------------------------------------------------

Stance = Literal["bullish", "bearish", "neutral"]
Side = Literal["long", "short", "flat"]
Instrument = Literal["spot", "perp"]
Verdict = Literal["approved", "downsized", "rejected"]
EntryType = Literal["market", "limit", "range"]
OrderStatus = Literal["new", "partially_filled", "filled", "canceled", "rejected", "failed"]
AgentId = Literal["technical", "derivatives", "sentiment", "onchain"]


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


# ---- 采集层：MarketSnapshot 及其组成 ---------------------------------------

class Candle(BaseModel):
    ts: datetime
    open: Decimal
    high: Decimal
    low: Decimal
    close: Decimal
    volume: Decimal


class OrderBook(BaseModel):
    bids: list[tuple[Decimal, Decimal]] = Field(default_factory=list)  # [price, size]
    asks: list[tuple[Decimal, Decimal]] = Field(default_factory=list)


class DerivativesData(BaseModel):
    """合约/永续维度（现货标的为 None）。"""

    funding_rate: Decimal | None = None       # 当期资金费率
    open_interest: Decimal | None = None       # 持仓量（名义）
    long_short_ratio: Decimal | None = None    # 多空比
    basis: Decimal | None = None               # 基差（合约-现货）
    liquidations_24h: Decimal | None = None    # 24h 爆仓额


class OnchainData(BaseModel):
    smart_money_flow: Decimal | None = None    # 聪明钱净流入（USD，正=流入）
    liquidity_usd: Decimal | None = None       # 相关 DEX 池深/流动性
    holder_concentration: Decimal | None = None  # 持有集中度 0..1
    exchange_netflow: Decimal | None = None    # 交易所净流（正=流入交易所=潜在抛压）


class SentimentData(BaseModel):
    fear_greed: int | None = Field(default=None, ge=0, le=100)  # 恐贪指数
    news_score: Decimal | None = Field(default=None, ge=-1, le=1)  # 新闻情绪 [-1,1]
    social_heat: Decimal | None = Field(default=None, ge=0)     # 社媒热度


class MarketSnapshot(BaseModel):
    """某标的在某场所某时刻的多维快照，喂给分析师团。"""

    symbol: str
    venue: str
    ts: datetime = Field(default_factory=_utcnow)
    ohlcv: list[Candle] = Field(default_factory=list)
    orderbook: OrderBook | None = None
    derivatives: DerivativesData | None = None
    onchain: OnchainData | None = None
    sentiment: SentimentData | None = None


# ---- 分析层：AnalystReport --------------------------------------------------

class Signal(BaseModel):
    name: str                 # 例如 "rsi", "funding_regime", "smart_money"
    value: str                # 结构化值的字符串表示（便于跨层与展示）
    note: str = ""


class AnalystReport(BaseModel):
    agent: AgentId
    stance: Stance
    confidence: float = Field(ge=0.0, le=1.0)
    signals: list[Signal] = Field(default_factory=list)
    rationale: str = ""
    degraded: bool = False    # 是否因缺数据/缺 LLM 而降级


# ---- 决策层：TradeProposal --------------------------------------------------

class PricePlan(BaseModel):
    type: EntryType = "market"
    price: Decimal | None = None   # limit 用
    low: Decimal | None = None     # range 用
    high: Decimal | None = None    # range 用


class TradeProposal(BaseModel):
    symbol: str
    venue: str
    side: Side
    instrument: Instrument = "spot"
    size_quote: Decimal = Field(ge=0)      # 计价币规模（如 USDT）
    leverage: float = Field(default=1.0, ge=1.0)  # 现货=1
    entry: PricePlan = Field(default_factory=PricePlan)
    stop_loss: Decimal | None = None       # ★ 必填；缺失会被风控引擎直接否决
    take_profit: list[Decimal] = Field(default_factory=list)
    confidence: float = Field(ge=0.0, le=1.0)
    thesis: str = ""
    supporting_reports: list[str] = Field(default_factory=list)


# ---- 风控层：RiskAssessment -------------------------------------------------

class RiskAssessment(BaseModel):
    verdict: Verdict
    hard_violations: list[str] = Field(default_factory=list)  # 触发的确定性规则
    adjusted_size_quote: Decimal | None = None   # downsized 时给出
    llm_notes: str = ""                          # 风控官软评审意见
    risk_score: float = Field(default=0.0, ge=0.0, le=1.0)
    llm_reviewed: bool = False                   # 是否经过软评审（降级时为 False）


# ---- 审批层：ApprovalDecision -----------------------------------------------

class ApprovalDecision(BaseModel):
    decision: Literal["approve", "reject", "modify"]
    modified: TradeProposal | None = None        # decision=modify 时的新提案
    operator: str = "system"
    ts: datetime = Field(default_factory=_utcnow)
    note: str = ""


# ---- 执行层：OrderIntent / ExecutionResult ----------------------------------

class OrderIntent(BaseModel):
    client_id: str                 # ★ 幂等键（交易所 clientOrderId / 链上去重）
    symbol: str
    venue: str
    side: Side
    instrument: Instrument = "spot"
    order_type: EntryType = "market"
    size_quote: Decimal = Field(ge=0)
    price: Decimal | None = None
    leverage: float = Field(default=1.0, ge=1.0)
    reduce_only: bool = False
    stop_loss: Decimal | None = None
    take_profit: list[Decimal] = Field(default_factory=list)


class ProtectiveOrder(BaseModel):
    """入场成交后挂在交易所侧的保护单（有仓必有保护）。"""

    kind: Literal["stop_loss", "take_profit"]
    order_id: str
    trigger_price: Decimal
    reduce_only: bool = True


class ExecutionResult(BaseModel):
    client_id: str
    order_id: str | None = None
    status: OrderStatus
    filled_base: Decimal = Field(default=Decimal(0), ge=0)
    avg_price: Decimal | None = None
    fee_quote: Decimal = Field(default=Decimal(0), ge=0)
    slippage_bps: Decimal | None = None
    protective_orders: list[ProtectiveOrder] = Field(default_factory=list)
    error: str | None = None


# ---- 账户/持仓（venue ↔ portfolio ↔ risk 复用）-----------------------------

class Position(BaseModel):
    symbol: str
    venue: str
    side: Literal["long", "short"]
    instrument: Instrument = "spot"
    size_base: Decimal = Field(ge=0)     # 基础币数量
    entry_price: Decimal
    leverage: float = Field(default=1.0, ge=1.0)

    def notional_at(self, price: Decimal) -> Decimal:
        return self.size_base * price


class Balances(BaseModel):
    quote_ccy: str = "USDT"
    free_quote: Decimal = Decimal(0)     # 可用计价币
    total_quote: Decimal = Decimal(0)    # 账户净值近似（含持仓市值）


# ---- 复盘层：TradeReview ----------------------------------------------------

class TradeReview(BaseModel):
    symbol: str
    proposal_ref: str = ""              # 关联提案/run 标识
    score: float = Field(default=0.0, ge=0.0, le=1.0)  # 本次决策质量评分
    pnl_quote: Decimal = Decimal(0)     # 已实现盈亏（计价币）
    slippage_bps: Decimal | None = None
    lessons: list[str] = Field(default_factory=list)   # 经验条目，回灌 memory
    notes: str = ""
    ts: datetime = Field(default_factory=_utcnow)
