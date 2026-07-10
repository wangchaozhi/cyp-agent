"""首席策略官：把多份 AnalystReport 合成一个 TradeProposal。

安全边界：方向/仓位/止损等**数字全部由规则确定**（可被风控引擎校验），
LLM 仅用于润色 thesis 文本，绝不改动交易参数——避免模型幻觉直接影响下单。
仓位按「单笔风险 ≤ 账户 × max_risk_per_trade」反推，止损用 ATR 定距。
"""

from __future__ import annotations

from decimal import ROUND_DOWN, Decimal
from typing import Literal

from pydantic import BaseModel, Field

from cyp.agents.base import AgentContext, stance_sign
from cyp.config import RiskConfig
from cyp.contracts import AnalystReport, MarketSnapshot, Position, PricePlan, TradeProposal
from cyp.data import ewma_vol_from_candles, indicator_snapshot
from cyp.portfolio import CorrelationModel, PortfolioView

DEFAULT_WEIGHTS = {"technical": 1.0, "derivatives": 0.9, "sentiment": 0.6, "onchain": 0.8}


class StrategyConfig(BaseModel):
    """策略官的可调参数——用于回测扫参择优。"""

    weights: dict[str, float] = Field(default_factory=lambda: dict(DEFAULT_WEIGHTS))
    enter_threshold: float = 0.12          # |综合分| 低于此则不开仓
    k_stop: Decimal = Decimal("2")         # 止损 = 波动度量 × k_stop
    k_tp: Decimal = Decimal("3")           # 止盈 = 波动度量 × k_tp
    risk_per_trade: Decimal | None = None  # 单笔风险预算；None → 用 RiskConfig.max_risk_per_trade
    stop_mode: Literal["atr", "vol"] = "atr"   # 止损波动度量：ATR 或 EWMA 波动率
    vol_lambda: float = 0.94               # EWMA 衰减因子（RiskMetrics）
    vol_target: float | None = None        # 每周期目标波动；设了则用波动目标仓位（size ∝ 目标/预测波动）


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
        positions: list[Position] | None = None,
    ) -> TradeProposal:
        sc = self.config
        positions = positions or []
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
        # 波动度量：ATR 或 EWMA 波动率（后者更有预测性，捕捉波动聚集）
        ewma_sigma = ewma_vol_from_candles(snap.ohlcv, sc.vol_lambda)
        if sc.stop_mode == "vol" and ewma_sigma > 0:
            vol_unit = ref * Decimal(str(ewma_sigma))       # 收益波动 → 价格波动
        else:
            vol_unit = Decimal(str(ind["atr"] or float(ref) * 0.02))
        atr_dec = vol_unit
        side = "long" if net > 0 else "short"

        # 组合感知①：同标的已有同向持仓 → 不加仓（主动规避，而非留给风控否决）
        if any(p.symbol == snap.symbol and p.side == side for p in positions):
            return TradeProposal(
                symbol=snap.symbol, venue=venue_id, side="flat", size_quote=Decimal(0),
                confidence=confidence,
                thesis=f"已持有 {snap.symbol} 同向（{side}）仓位，本轮不加仓。",
                supporting_reports=[r.agent for r in reports if not r.degraded],
            )

        # 工具与杠杆：工具必须与标的形态一致（永续符号含 ':'，如 BTC/USDT:USDT）——
        # 永续符号只能走 perp（现货参数下单必被交易所拒），现货符号无法裸卖空。
        max_lev = float(cfg.max_leverage)
        allow_perp = getattr(ctx.settings, "allow_perp", False)
        is_perp_symbol = ":" in snap.symbol
        if is_perp_symbol and not allow_perp:
            return TradeProposal(
                symbol=snap.symbol, venue=venue_id, side="flat", size_quote=Decimal(0),
                confidence=confidence,
                thesis=f"{snap.symbol} 为永续合约标的，但未开启 allow_perp，本轮不交易。",
                supporting_reports=[r.agent for r in reports if not r.degraded],
            )
        if is_perp_symbol:
            instrument = "perp"     # 杠杆仍由置信度决定：低置信 = 1x，不因符号形态加杠杆
            leverage = max(1.0, min(max_lev, float(round(1 + confidence * (max_lev - 1))))) \
                if confidence >= 0.25 and max_lev > 1 else 1.0
        elif allow_perp and confidence >= 0.25 and max_lev > 1:
            instrument = "perp"
            leverage = max(1.0, min(max_lev, float(round(1 + confidence * (max_lev - 1)))))
        else:
            instrument = "spot"
            leverage = 1.0
        if side == "short" and instrument == "spot":
            return TradeProposal(
                symbol=snap.symbol, venue=venue_id, side="flat", size_quote=Decimal(0),
                confidence=confidence,
                thesis="看空信号但现货无法裸卖空（无持仓可减），本轮观望。",
                supporting_reports=[r.agent for r in reports if not r.degraded],
            )
        if side == "long":
            stop = ref - sc.k_stop * atr_dec
            tps = [ref + sc.k_tp * atr_dec]
        else:
            stop = ref + sc.k_stop * atr_dec
            tps = [ref - sc.k_tp * atr_dec]

        stop_frac = abs(ref - stop) / ref if ref > 0 else Decimal("0.02")
        # 仓位：波动目标（size ∝ 目标波动/预测波动）或固定风险预算（默认）
        if sc.vol_target and ewma_sigma > 0:
            size_quote = equity * Decimal(str(sc.vol_target)) / Decimal(str(ewma_sigma))
        else:
            risk_budget = equity * (sc.risk_per_trade or cfg.max_risk_per_trade)
            size_quote = (risk_budget / stop_frac) if stop_frac > 0 else Decimal(0)
        # 主动贴合硬护栏（规避而非留给风控缩仓/否决）：
        # ×0.995 防 Decimal 量化后恰好压线超预算；再夹到单仓名义上限
        size_quote = min(size_quote * Decimal("0.995"), equity * cfg.max_position_pct)

        # 组合感知②：相关性簇同向敞口接近上限（≥80%）→ 按剩余额度缩仓，额度耗尽则不开
        corr = CorrelationModel()
        cluster = corr.cluster_of(snap.symbol)
        cluster_exp = PortfolioView(positions, corr).cluster_net_directional(cluster, side)
        cluster_limit = equity * cfg.max_correlated_exposure
        portfolio_note = ""
        if cluster_limit > 0 and cluster_exp >= cluster_limit * Decimal("0.8"):
            headroom = cluster_limit - cluster_exp
            if headroom <= 0:
                return TradeProposal(
                    symbol=snap.symbol, venue=venue_id, side="flat", size_quote=Decimal(0),
                    confidence=confidence,
                    thesis=f"{cluster} 簇同向敞口已达上限（{cluster_exp}/{cluster_limit}），本轮规避。",
                    supporting_reports=[r.agent for r in reports if not r.degraded],
                )
            size_quote = min(size_quote, headroom)
            portfolio_note = f"（{cluster} 簇敞口接近上限，已按剩余额度缩仓）"

        thesis = self._thesis(reports, net, side) + portfolio_note
        if ctx.llm.enabled:  # LLM 只润色文本，不改数字
            held = ", ".join(f"{p.symbol}:{p.side}" for p in positions) or "无"
            lessons = "；".join(ctx.lessons[-5:]) or "无"
            refined = await ctx.llm.text(
                system="你是加密交易策略官，用两句话中文说明该交易的核心逻辑。"
                       "方向与仓位已由规则确定，你只解释依据：不要建议观望、不要否定或反转方向，"
                       "不要给出与输入不同的价格或仓位。",
                user=f"方向={side} 综合分={net:.2f} 当前组合持仓={held} "
                     f"历史复盘经验={lessons} 依据={thesis}", fast=True)
            if refined:
                thesis = refined.strip()

        return TradeProposal(
            symbol=snap.symbol, venue=venue_id, side=side, instrument=instrument,
            # 向下取整：防四舍五入把已贴线的仓位抬回护栏之上
            size_quote=size_quote.quantize(Decimal("0.01"), rounding=ROUND_DOWN), leverage=leverage,
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
