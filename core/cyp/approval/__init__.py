"""人工审批门：半自动的核心关卡——每笔真实下单必经人工。

M0 提供 AutoApprove/AutoReject（测试与全自动占位）；CLI 版见 cyp.approval.cli。
超时策略：开仓审批超时=拒绝（fail-safe，见 docs/RISK.md）。
"""

from cyp.approval.gate import ApprovalGate, AutoApprove, AutoReject, PolicyApprovalGate
from cyp.approval.server import PendingApprovalGate

__all__ = ["ApprovalGate", "AutoApprove", "AutoReject", "PolicyApprovalGate", "PendingApprovalGate"]


def wrap_with_policy(settings, human: ApprovalGate) -> ApprovalGate:
    """按 CYP_APPROVAL 接线：auto → 策略化自动审批（不满足策略仍转 human），否则原样返回。"""
    if settings.approval != "auto":
        return human
    return PolicyApprovalGate(
        fallback=human,
        symbols=settings.auto_symbols_list(),
        max_risk_score=settings.auto_max_risk_score,
        max_quote=settings.auto_max_quote,
    )
