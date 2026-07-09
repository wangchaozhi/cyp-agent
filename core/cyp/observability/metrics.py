"""运行指标：结果计数 + 成交/滑点聚合 + SLO（审批时延/滑点分布/下单成功率）。

供 GET /api/metrics 与巡检（M6：SLO 细化）。
"""

from __future__ import annotations

from dataclasses import dataclass, field
from decimal import Decimal

# 滑点分桶边界（bps）：0-5 / 5-15 / 15-30 / 30+
_SLIPPAGE_BUCKETS = (Decimal(5), Decimal(15), Decimal(30))
_BUCKET_LABELS = ("0-5", "5-15", "15-30", "30+")


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
    _slippage_hist: dict = field(default_factory=lambda: dict.fromkeys(_BUCKET_LABELS, 0))
    _approval_latency_sum: float = 0.0
    _approval_latency_max: float = 0.0
    _approval_latency_n: int = 0

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
            for bound, label in zip(_SLIPPAGE_BUCKETS, _BUCKET_LABELS, strict=False):
                if slippage_bps < bound:
                    self._slippage_hist[label] += 1
                    break
            else:
                self._slippage_hist["30+"] += 1

    def record_approval_latency(self, seconds: float) -> None:
        self._approval_latency_sum += seconds
        self._approval_latency_max = max(self._approval_latency_max, seconds)
        self._approval_latency_n += 1

    @property
    def avg_slippage_bps(self) -> float:
        return float(self._slippage_sum_bps / self._slippage_n) if self._slippage_n else 0.0

    @property
    def approval_rate(self) -> float:
        decided = self.executed + self.not_approved
        return round(self.executed / decided, 3) if decided else 0.0

    @property
    def order_success_rate(self) -> float:
        attempted = self.executed + self.errors
        return round(self.executed / attempted, 3) if attempted else 0.0

    @property
    def avg_approval_latency_s(self) -> float:
        return round(self._approval_latency_sum / self._approval_latency_n, 3) \
            if self._approval_latency_n else 0.0

    def snapshot(self) -> dict:
        return {"runs": self.runs, "executed": self.executed, "rejected": self.rejected,
                "not_approved": self.not_approved, "no_trade": self.no_trade, "errors": self.errors,
                "avg_slippage_bps": round(self.avg_slippage_bps, 2), "approval_rate": self.approval_rate,
                "order_success_rate": self.order_success_rate,
                "slippage_hist_bps": dict(self._slippage_hist),
                "approval_latency": {"avg_s": self.avg_approval_latency_s,
                                     "max_s": round(self._approval_latency_max, 3),
                                     "n": self._approval_latency_n}}
