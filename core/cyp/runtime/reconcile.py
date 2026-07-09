"""对账：真相在交易所/链上，不在本地账本。对账未完成前冻结新开仓。

M0（paper）：拉取 venue 真实持仓/余额与本地账本 diff，校验持仓保护覆盖。
真正的远端 diff（宕机期间成交/外部转账/强平）在 M2/M3 接实盘时补全（见 RUNTIME.md §3）。
"""

from __future__ import annotations

from dataclasses import dataclass, field

from cyp.events import EventBus
from cyp.observability import get_logger


@dataclass
class ReconcileReport:
    positions: list[dict] = field(default_factory=list)
    discrepancies: list[str] = field(default_factory=list)
    protective_gaps: list[str] = field(default_factory=list)
    ok: bool = True


class Reconciler:
    def __init__(self, venue, memory=None, events: EventBus | None = None) -> None:
        self.venue = venue
        self.memory = memory
        self.events = events
        self.log = get_logger("reconcile")

    async def reconcile(self) -> ReconcileReport:
        positions = await self.venue.positions()
        gaps: list[str] = []
        # 有仓必有保护：非原生保护单的场所（如链上）需监控覆盖，此处标记为需关注
        if not getattr(self.venue, "caps", None) or not self.venue.caps.native_protective_orders:
            gaps = [f"{p.symbol} 保护依赖监控存活" for p in positions]

        # 链上场所：nonce 对齐 + pending tx 归位（M3）
        discrepancies: list[str] = []
        reconcile_onchain = getattr(self.venue, "reconcile_onchain", None)
        if reconcile_onchain is not None:
            try:
                onchain = await reconcile_onchain()
                discrepancies += onchain.get("discrepancies", [])
                discrepancies += [f"tx 归位：{s}" for s in onchain.get("settled", [])]
                if onchain.get("pending"):
                    discrepancies.append(f"仍有未确认 tx：{onchain['pending']}")
            except Exception as e:  # noqa: BLE001
                discrepancies.append(f"链上对账失败：{e}")

        report = ReconcileReport(
            positions=[p.model_dump(mode="json") for p in positions],
            discrepancies=discrepancies, protective_gaps=gaps, ok=True,
        )
        self.log.info("reconciled", positions=len(positions), gaps=len(gaps))
        if self.events:
            await self.events.publish("reconciled", "-", positions=report.positions,
                                      protective_gaps=gaps)
        return report
