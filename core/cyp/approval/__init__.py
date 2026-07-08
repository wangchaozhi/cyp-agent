"""人工审批门：半自动的核心关卡——每笔真实下单必经人工。

M0 提供 AutoApprove/AutoReject（测试与全自动占位）；CLI 版见 cyp.approval.cli。
超时策略：开仓审批超时=拒绝（fail-safe，见 docs/RISK.md）。
"""

from cyp.approval.gate import ApprovalGate, AutoApprove, AutoReject
from cyp.approval.server import PendingApprovalGate

__all__ = ["ApprovalGate", "AutoApprove", "AutoReject", "PendingApprovalGate"]
