"""交易员：把已审批的提案忠实执行。不做决策，只负责下单与订单生命周期。

- 幂等：client_id 贯穿到 venue（重放不重复成交）。
- 入场即带止损/止盈（venue 侧挂原生保护单，见 RUNTIME.md「有仓必有保护」）。
- size_quote 用风控裁决后的最终值（downsized 时为调整后规模）。
"""

from __future__ import annotations

from decimal import Decimal

from cyp.contracts import ExecutionResult, OrderIntent, TradeProposal


class Trader:
    id = "trader"

    async def execute(
        self,
        proposal: TradeProposal,
        venue,
        client_id: str,
        size_quote: Decimal | None = None,
    ) -> ExecutionResult:
        if proposal.side == "flat":
            return ExecutionResult(client_id=client_id, status="filled", filled_base=Decimal(0))

        intent = OrderIntent(
            client_id=client_id,
            symbol=proposal.symbol,
            venue=getattr(venue, "id", proposal.venue),
            side=proposal.side,
            instrument=proposal.instrument,
            order_type=proposal.entry.type,
            size_quote=size_quote if size_quote is not None else proposal.size_quote,
            price=proposal.entry.price,
            leverage=proposal.leverage,
            stop_loss=proposal.stop_loss,
            take_profit=proposal.take_profit,
        )
        return await venue.place(intent)
