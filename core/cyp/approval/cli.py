"""CLIApprovalGate：终端人工审批。展示提案+风控结论，等待 批准/拒绝/修改。

超时策略：开仓审批超时 = 拒绝（fail-safe，见 docs/RISK.md）。
input_fn/print_fn 可注入以便测试。
"""

from __future__ import annotations

import asyncio
from decimal import Decimal, InvalidOperation
from typing import Callable

from cyp.contracts import ApprovalDecision, RiskAssessment, TradeProposal


class CLIApprovalGate:
    def __init__(self, timeout: float = 1800, operator: str = "cli",
                 input_fn: Callable[[str], str] = input,
                 print_fn: Callable[[str], None] = print) -> None:
        self._timeout = timeout
        self._operator = operator
        self._input_fn = input_fn
        self._print = print_fn

    async def decide(self, proposal: TradeProposal, assessment: RiskAssessment) -> ApprovalDecision:
        self._render(proposal, assessment)
        raw = await self._prompt()
        return self._parse(raw, proposal)

    def _render(self, p: TradeProposal, a: RiskAssessment) -> None:
        size = a.adjusted_size_quote or p.size_quote
        self._print("\n" + "=" * 56)
        self._print(f"  待审批  {p.side.upper()} {p.symbol}  @ {p.venue}")
        self._print(f"  规模={size} {('(风控缩仓后)' if a.adjusted_size_quote else '')}  杠杆={p.leverage}x")
        self._print(f"  止损={p.stop_loss}  止盈={p.take_profit}  置信={p.confidence:.2f}")
        self._print(f"  风控={a.verdict}  risk_score={a.risk_score:.2f}  软评审={a.llm_reviewed}")
        if a.hard_violations:
            self._print(f"  护栏提示：{'; '.join(a.hard_violations)}")
        self._print(f"  论述：{p.thesis}")
        self._print("=" * 56)

    async def _prompt(self) -> str:
        loop = asyncio.get_event_loop()
        try:
            return await asyncio.wait_for(
                loop.run_in_executor(None, self._input_fn, "决策 [approve / reject / modify <规模>]: "),
                timeout=self._timeout,
            )
        except asyncio.TimeoutError:
            self._print("审批超时 → 拒绝（fail-safe）")
            return "reject"

    def _parse(self, raw: str, proposal: TradeProposal) -> ApprovalDecision:
        text = (raw or "").strip().lower()
        if text.startswith("a"):
            return ApprovalDecision(decision="approve", operator=self._operator, note="人工批准")
        if text.startswith("m"):
            parts = text.split()
            if len(parts) >= 2:
                try:
                    new_size = Decimal(parts[1])
                    modified = proposal.model_copy(update={"size_quote": new_size})
                    return ApprovalDecision(decision="modify", modified=modified,
                                            operator=self._operator, note=f"人工改规模至 {new_size}")
                except (InvalidOperation, ValueError):
                    pass
            self._print("modify 需带规模，如 'modify 500'；未识别 → 拒绝")
        return ApprovalDecision(decision="reject", operator=self._operator, note="人工拒绝")
