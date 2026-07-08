"""首席策略官：把多份 AnalystReport 合成一个 TradeProposal。

安全边界：方向/仓位/止损等**数字全部由规则确定**（可被风控引擎校验），
LLM 仅用于润色 thesis 文本，绝不改动交易参数——避免模型幻觉直接影响下单。
仓位按「单笔风险 ≤ 账户 × max_risk_per_trade」反推，止损用 ATR 定距。
"""

from __future__ import annotations

from decimal import Decimal

from pydantic import BaseModel, Field

from cyp.agents.base import AgentContext, stance_sign
from cyp.config import RiskConfig
from cyp.contracts import AnalystReport, MarketSnapshot, PricePlan, TradeProposal
from cyp.data import indicator_snapshot

DEFAULT_WEIGHTS = {"technical": 1.0, "derivatives": 0.9, "sentiment": 0.6, "onchain": 0.8}


class StrategyConfig(BaseModel):
    """策略官的可调参数——用于回测扫参择优。"""

    weights: dict[str, float] = Field(default_factory=lambda: dict(DEFAULT_WEIGHTS))
    enter_threshold: float = 0.12          # |综合分| 低于此则不开仓
    k_stop: Decimal = Decimal("2")         # 止损 = ATR × k_stop
    k_tp: Decimal = Decimal("3")           # 止盈 = ATR × k_tp
    risk_per_trade: Decimal | None = None  # 单笔风险预算；None → 用 RiskConfig.max_risk_per_trade


class Strategist:
    id = "strategist"

    def __init__(self, config: StrategyConfig | None = None) -> None:
        self.config = config or StrategyConfig()

    async def run(
        self,
        reports: list[AnalystReport],
        snap: MarketSnapshot,
        equity: Decimal,
        cfg: RiskConfig,
        ctx: AgentContext,
        venue_id: str = "paper",
    ) -> TradeProposal:
        sc = self.config
        # 1) 加权综合（跳过降级维度）
        contribs = [(stance_sign(r.stance) * r.confidence, sc.weights.get(r.agent, 0.5))
                    for r in reports if not r.degraded]
        tot_w = sum(w for _, w in contribs)
        net = sum(s * w for s, w in contribs) / tot_w if tot_w > 0 else 0.0
        confidence = min(1.0, abs(net))

        ind = indicator_snapshot(snap.ohlcv)
        last_close = ind["last_close"]
        if last_close is None or abs(net) < sc.enter_threshold:
            return TradeProposal(
                symbol=snap.symbol, venue=venue_id, side="flat", size_quote=Decimal(0),
                confidence=confidence, thesis="多维信号不足或冲突，本轮不开仓。",
                supporting_reports=[r.agent for r in reports if not r.degraded],
            )

        ref = Decimal(str(last_close))
        atr = ind["atr"] or float(ref) * 0.02
        atr_dec = Decimal(str(atr))
        side = "long" if net > 0 else "short"

        # 工具与杠杆：允许永续且置信足够时用合约，杠杆由置信度决定（封顶 max_leverage）
        max_lev = float(cfg.max_leverage)
        if getattr(ctx.settings, "allow_perp", False) and confidence >= 0.25 and max_lev > 1:
            instrument = "perp"
            leverage = max(1.0, min(max_lev, float(round(1 + confidence * (max_lev - 1)))))
        else:
            instrument = "spot"
            leverage = 1.0
        if side == "long":
            stop = ref - sc.k_stop * atr_dec
            tps = [ref + sc.k_tp * atr_dec]
        else:
            stop = ref + sc.k_stop * atr_dec
            tps = [ref - sc.k_tp * atr_dec]

        stop_frac = abs(ref - stop) / ref if ref > 0 else Decimal("0.02")
        risk_budget = equity * (sc.risk_per_trade or cfg.max_risk_per_trade)
        size_quote = (risk_budget / stop_frac) if stop_frac > 0 else Decimal(0)

        thesis = self._thesis(reports, net, side)
        if ctx.llm.enabled:  # LLM 只润色文本，不改数字
            refined = await ctx.llm.text(
                system="你是加密交易策略官，用两句话中文说明该交易的核心逻辑，不要给出与输入不同的价格或仓位。",
                user=f"方向={side} 综合分={net:.2f} 依据={thesis}", fast=True)
            if refined:
                thesis = refined.strip()

        return TradeProposal(
            symbol=snap.symbol, venue=venue_id, side=side, instrument=instrument,
            size_quote=size_quote.quantize(Decimal("0.01")), leverage=leverage,
            margin_mode="isolated",
            entry=PricePlan(type="market"),
            stop_loss=stop.quantize(Decimal("0.01")),
            take_profit=[t.quantize(Decimal("0.01")) for t in tps],
            confidence=confidence, thesis=thesis,
            supporting_reports=[r.agent for r in reports if not r.degraded],
        )

    def _thesis(self, reports: list[AnalystReport], net: float, side: str) -> str:
        drivers = ", ".join(f"{r.agent}:{r.stance}" for r in reports if not r.degraded)
        return f"综合分 {net:+.2f} → {side}。驱动：{drivers}。"
