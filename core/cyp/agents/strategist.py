"""首席策略官：把多份 AnalystReport 合成一个 TradeProposal。

安全边界：方向/仓位/止损等**数字全部由规则确定**（可被风控引擎校验），
LLM 仅用于润色 thesis 文本，绝不改动交易参数——避免模型幻觉直接影响下单。
仓位按「单笔风险 ≤ 账户 × max_risk_per_trade」反推，止损用 ATR 定距。
"""

from __future__ import annotations

from decimal import Decimal

from cyp.agents.base import AgentContext, stance_sign
from cyp.config import RiskConfig
from cyp.contracts import AnalystReport, MarketSnapshot, PricePlan, TradeProposal
from cyp.data import indicator_snapshot

AGENT_WEIGHT = {"technical": 1.0, "derivatives": 0.9, "sentiment": 0.6, "onchain": 0.8}
_ENTER_THRESHOLD = 0.12
K_STOP = Decimal("2")
K_TP = Decimal("3")


class Strategist:
    id = "strategist"

    async def run(
        self,
        reports: list[AnalystReport],
        snap: MarketSnapshot,
        equity: Decimal,
        cfg: RiskConfig,
        ctx: AgentContext,
        venue_id: str = "paper",
    ) -> TradeProposal:
        # 1) 加权综合（跳过降级维度）
        contribs = [(stance_sign(r.stance) * r.confidence, AGENT_WEIGHT.get(r.agent, 0.5))
                    for r in reports if not r.degraded]
        tot_w = sum(w for _, w in contribs)
        net = sum(s * w for s, w in contribs) / tot_w if tot_w > 0 else 0.0
        confidence = min(1.0, abs(net))

        ind = indicator_snapshot(snap.ohlcv)
        last_close = ind["last_close"]
        if last_close is None or abs(net) < _ENTER_THRESHOLD:
            return TradeProposal(
                symbol=snap.symbol, venue=venue_id, side="flat", size_quote=Decimal(0),
                confidence=confidence, thesis="多维信号不足或冲突，本轮不开仓。",
                supporting_reports=[r.agent for r in reports if not r.degraded],
            )

        ref = Decimal(str(last_close))
        atr = ind["atr"] or float(ref) * 0.02
        atr_dec = Decimal(str(atr))
        side = "long" if net > 0 else "short"
        if side == "long":
            stop = ref - K_STOP * atr_dec
            tps = [ref + K_TP * atr_dec]
        else:
            stop = ref + K_STOP * atr_dec
            tps = [ref - K_TP * atr_dec]

        stop_frac = abs(ref - stop) / ref if ref > 0 else Decimal("0.02")
        risk_budget = equity * cfg.max_risk_per_trade
        size_quote = (risk_budget / stop_frac) if stop_frac > 0 else Decimal(0)

        thesis = self._thesis(reports, net, side)
        if ctx.llm.enabled:  # LLM 只润色文本，不改数字
            refined = await ctx.llm.text(
                system="你是加密交易策略官，用两句话中文说明该交易的核心逻辑，不要给出与输入不同的价格或仓位。",
                user=f"方向={side} 综合分={net:.2f} 依据={thesis}", fast=True)
            if refined:
                thesis = refined.strip()

        return TradeProposal(
            symbol=snap.symbol, venue=venue_id, side=side, instrument="spot",
            size_quote=size_quote.quantize(Decimal("0.01")), leverage=1.0,
            entry=PricePlan(type="market"),
            stop_loss=stop.quantize(Decimal("0.01")),
            take_profit=[t.quantize(Decimal("0.01")) for t in tps],
            confidence=confidence, thesis=thesis,
            supporting_reports=[r.agent for r in reports if not r.degraded],
        )

    def _thesis(self, reports: list[AnalystReport], net: float, side: str) -> str:
        drivers = ", ".join(f"{r.agent}:{r.stance}" for r in reports if not r.degraded)
        return f"综合分 {net:+.2f} → {side}。驱动：{drivers}。"
