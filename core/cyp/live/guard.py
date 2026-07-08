"""LiveGuard：实盘前置校验。任一不满足 → 退回只读（安全默认），绝不误开实盘。

校验项：
- 有交易所 API Key（否则无法下单）。
- 显式实盘确认 CYP_LIVE_ACK=1（防手滑）。
- Kill Switch 未开启。
提现权限须在交易所侧禁用 + 开 IP 白名单（无法由客户端校验，见 docs/RISK.md）。
"""

from __future__ import annotations

from dataclasses import dataclass, field

from cyp.config import Settings


@dataclass
class LiveGuardReport:
    ok: bool
    reasons: list[str] = field(default_factory=list)


class LiveGuard:
    @staticmethod
    def check(settings: Settings) -> LiveGuardReport:
        if settings.mode != "live":
            return LiveGuardReport(ok=True)   # paper 无需校验
        reasons: list[str] = []
        if not settings.cex_trading_enabled:
            reasons.append("缺少交易所 API Key，无法实盘（保持只读）")
        if not settings.live_ack:
            reasons.append("未确认实盘：请设置 CYP_LIVE_ACK=1")
        if settings.kill:
            reasons.append("Kill Switch 开启，禁止实盘")
        return LiveGuardReport(ok=not reasons, reasons=reasons)
