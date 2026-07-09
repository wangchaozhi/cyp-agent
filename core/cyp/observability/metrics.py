"""运行指标：结果计数 + 成交/滑点聚合，供 GET /api/metrics 与巡检。"""

from __future__ import annotations

from dataclasses import dataclass, field
from decimal import Decimal


@dataclass
class RunMetrics:
    runs: int = 0
    executed: int = 0
    rejected: int = 0
    not_approved: int = 0
    no_trade: int = 0
    errors: int = 0
    _slippage_sum_bps: Decimal = field(default=Decimal(0))
    _slippage_n: int = 0

    _STATUS_ATTR = {"executed": "executed", "rejected": "rejected",
                    "not_approved": "not_approved", "no_trade": "no_trade",
                    "execution_failed": "errors", "error": "errors"}

    def record(self, status: str, slippage_bps: Decimal | None = None) -> None:
        self.runs += 1
        attr = self._STATUS_ATTR.get(status, "errors")
        setattr(self, attr, getattr(self, attr) + 1)
        if slippage_bps is not None:
            self._slippage_sum_bps += slippage_bps
            self._slippage_n += 1

    @property
    def avg_slippage_bps(self) -> float:
        return float(self._slippage_sum_bps / self._slippage_n) if self._slippage_n else 0.0

    @property
    def approval_rate(self) -> float:
        decided = self.executed + self.not_approved
        return round(self.executed / decided, 3) if decided else 0.0

    def snapshot(self) -> dict:
        return {"runs": self.runs, "executed": self.executed, "rejected": self.rejected,
                "not_approved": self.not_approved, "no_trade": self.no_trade, "errors": self.errors,
                "avg_slippage_bps": round(self.avg_slippage_bps, 2), "approval_rate": self.approval_rate}
