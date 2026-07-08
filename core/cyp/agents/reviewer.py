"""复盘官：对本轮决策与执行做归因，产出结构化经验条目回灌记忆。

M0：入场后即时复盘（执行质量：是否成交、滑点是否在容忍内）。
盈亏归因（完整 round-trip PnL）在持仓平仓后由监控循环触发（M1+）。
"""

from __future__ import annotations

from decimal import Decimal

from cyp.agents.base import AgentContext
from cyp.contracts import ExecutionResult, TradeProposal, TradeReview


class Reviewer:
    id = "reviewer"

    async def run(
        self,
        proposal: TradeProposal,
        result: ExecutionResult,
        ctx: AgentContext,
        run_id: str = "",
    ) -> TradeReview:
        lessons: list[str] = []
        if result.status != "filled":
            score = 0.2
            lessons.append(f"执行失败（{result.status}）：{result.error or '未知'}，检查 preflight 与场所可用性。")
        else:
            score = 0.6
            slip = result.slippage_bps
            if slip is not None and slip > Decimal("20"):
                score -= 0.2
                lessons.append(f"滑点偏高 {slip}bps，考虑限价入场或拆单。")
            if proposal.confidence < 0.3:
                lessons.append("入场置信度偏低，信号偏弱时可缩仓或观望。")

        return TradeReview(
            symbol=proposal.symbol,
            proposal_ref=run_id,
            score=max(0.0, min(1.0, score)),
            pnl_quote=Decimal(0),  # 未平仓，round-trip PnL 留待监控循环
            slippage_bps=result.slippage_bps,
            lessons=lessons,
            notes=f"{proposal.side} {proposal.symbol} 执行={result.status}",
        )
