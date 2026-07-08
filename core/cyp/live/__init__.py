"""实盘前置校验：mode=live 前必须满足的安全条件（否则退回只读，绝不误开实盘）。"""

from cyp.live.guard import LiveGuard, LiveGuardReport

__all__ = ["LiveGuard", "LiveGuardReport"]
