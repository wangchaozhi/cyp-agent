"""分析师团：技术面 / 衍生品 / 情绪 / 链上。并行、失败隔离、缺数据即降级。

每位分析师规则可独立产出结构化信号（无 LLM 也能跑）；缺对应数据维度时
标 degraded=True 且置信度压低，由策略官按权重综合。
"""

from __future__ import annotations

from decimal import Decimal
from typing import Protocol

from cyp.agents.base import AgentContext, Vote, blend
from cyp.contracts import AgentId, AnalystReport, MarketSnapshot, Signal
from cyp.data import indicator_snapshot


class Analyst(Protocol):
    id: AgentId

    async def run(self, snap: MarketSnapshot, ctx: AgentContext) -> AnalystReport: ...


class TechnicalAnalyst:
    id: AgentId = "technical"

    async def run(self, snap: MarketSnapshot, ctx: AgentContext) -> AnalystReport:
        ind = indicator_snapshot(snap.ohlcv)
        if ind["last_close"] is None:
            return AnalystReport(agent="technical", stance="neutral", confidence=0.2,
                                 rationale="无 K 线数据", degraded=True)
        votes: list[Vote] = []
        sf, ss = ind["sma_fast"], ind["sma_slow"]
        if sf is not None and ss is not None:
            sign = 1.0 if sf > ss else -1.0
            votes.append(Vote(sign, 1.0, Signal(name="trend", value=f"sma20{'>' if sign > 0 else '<'}sma50")))
        macd, sig = ind["macd"], ind["macd_signal"]
        if macd is not None and sig is not None:
            s = 1.0 if macd > sig else -1.0
            votes.append(Vote(s, 0.8, Signal(name="macd", value=f"{macd:.2f} vs {sig:.2f}")))
        rsi = ind["rsi"]
        if rsi is not None:
            if rsi > 70:
                votes.append(Vote(-1.0, 0.6, Signal(name="rsi", value=f"{rsi:.1f} 超买")))
            elif rsi < 30:
                votes.append(Vote(1.0, 0.6, Signal(name="rsi", value=f"{rsi:.1f} 超卖")))
            else:
                votes.append(Vote(0.0, 0.3, Signal(name="rsi", value=f"{rsi:.1f} 中性")))
        lc, bu, bl = ind["last_close"], ind["bb_upper"], ind["bb_lower"]
        if bu is not None and bl is not None:
            if lc > bu:
                votes.append(Vote(-1.0, 0.4, Signal(name="bollinger", value="上轨外，超伸")))
            elif lc < bl:
                votes.append(Vote(1.0, 0.4, Signal(name="bollinger", value="下轨外，超跌")))
        stance, conf = blend(votes)
        return AnalystReport(agent="technical", stance=stance, confidence=conf,
                             signals=[v.signal for v in votes],
                             rationale=f"技术面：RSI={rsi:.1f} MACD={'金叉' if (macd or 0) > (sig or 0) else '死叉'}"
                             if rsi is not None else "技术面综合")


class DerivativesAnalyst:
    id: AgentId = "derivatives"

    async def run(self, snap: MarketSnapshot, ctx: AgentContext) -> AnalystReport:
        d = snap.derivatives
        if d is None or d.funding_rate is None:
            return AnalystReport(agent="derivatives", stance="neutral", confidence=0.2,
                                 rationale="无衍生品数据", degraded=True)
        votes: list[Vote] = []
        fr = d.funding_rate
        # 资金费为正=多头拥挤（逆向偏空）；为负=空头拥挤（逆向偏多）
        if fr > Decimal("0.0003"):
            votes.append(Vote(-1.0, 0.8, Signal(name="funding", value=f"{fr} 偏高，多头拥挤")))
        elif fr < Decimal("-0.0003"):
            votes.append(Vote(1.0, 0.8, Signal(name="funding", value=f"{fr} 偏低，空头拥挤")))
        else:
            votes.append(Vote(0.0, 0.4, Signal(name="funding", value=f"{fr} 正常")))
        if d.long_short_ratio is not None:
            lsr = d.long_short_ratio
            if lsr > Decimal("1.15"):
                votes.append(Vote(-0.5, 0.5, Signal(name="ls_ratio", value=f"{lsr} 多头偏拥挤")))
            elif lsr < Decimal("0.87"):
                votes.append(Vote(0.5, 0.5, Signal(name="ls_ratio", value=f"{lsr} 空头偏拥挤")))
        stance, conf = blend(votes)
        return AnalystReport(agent="derivatives", stance=stance, confidence=conf,
                             signals=[v.signal for v in votes], rationale=f"衍生品：资金费 {fr}")


class SentimentAnalyst:
    id: AgentId = "sentiment"

    async def run(self, snap: MarketSnapshot, ctx: AgentContext) -> AnalystReport:
        s = snap.sentiment
        if s is None or s.fear_greed is None:
            return AnalystReport(agent="sentiment", stance="neutral", confidence=0.2,
                                 rationale="无情绪数据", degraded=True)
        votes: list[Vote] = []
        fg = s.fear_greed
        # 极端恐惧=逆向偏多；极端贪婪=逆向偏空
        if fg < 25:
            votes.append(Vote(1.0, 0.6, Signal(name="fear_greed", value=f"{fg} 极端恐惧")))
        elif fg > 75:
            votes.append(Vote(-1.0, 0.6, Signal(name="fear_greed", value=f"{fg} 极端贪婪")))
        else:
            votes.append(Vote(0.0, 0.3, Signal(name="fear_greed", value=f"{fg} 中性")))
        if s.news_score is not None:
            votes.append(Vote(float(s.news_score), 0.4, Signal(name="news", value=f"{s.news_score}")))
        stance, conf = blend(votes)
        return AnalystReport(agent="sentiment", stance=stance, confidence=conf,
                             signals=[v.signal for v in votes], rationale=f"情绪：恐贪 {fg}")


class OnchainAnalyst:
    id: AgentId = "onchain"

    async def run(self, snap: MarketSnapshot, ctx: AgentContext) -> AnalystReport:
        o = snap.onchain
        if o is None or o.smart_money_flow is None:
            return AnalystReport(agent="onchain", stance="neutral", confidence=0.2,
                                 rationale="无链上数据（M0 未接入）", degraded=True)
        votes: list[Vote] = []
        smf = o.smart_money_flow
        if smf > 0:
            votes.append(Vote(1.0, 0.8, Signal(name="smart_money", value=f"净流入 {smf}")))
        elif smf < 0:
            votes.append(Vote(-1.0, 0.8, Signal(name="smart_money", value=f"净流出 {smf}")))
        if o.exchange_netflow is not None:
            # 流入交易所=潜在抛压（偏空）
            sign = -1.0 if o.exchange_netflow > 0 else 1.0
            votes.append(Vote(sign, 0.5, Signal(name="exch_netflow", value=f"{o.exchange_netflow}")))
        stance, conf = blend(votes)
        return AnalystReport(agent="onchain", stance=stance, confidence=conf,
                             signals=[v.signal for v in votes], rationale="链上：聪明钱流向")


ANALYSTS: tuple[Analyst, ...] = (
    TechnicalAnalyst(), DerivativesAnalyst(), SentimentAnalyst(), OnchainAnalyst()
)
