"""事件总线：编排器每步发事件 → 订阅者（CLI 打印 / SSE 转发 / 记录）。

订阅回调可同步或异步。M0 用于 CLI 输出与测试断言，M2 接 SSE 推仪表盘。
"""

from __future__ import annotations

import inspect
from datetime import datetime, timezone
from typing import Any, Awaitable, Callable

EventHandler = Callable[[dict], Any | Awaitable[Any]]


class EventBus:
    def __init__(self) -> None:
        self._subs: list[EventHandler] = []

    def subscribe(self, handler: EventHandler) -> None:
        self._subs.append(handler)

    async def publish(self, type: str, run_id: str, **data: Any) -> None:
        event = {"type": type, "run_id": run_id,
                 "ts": datetime.now(timezone.utc).isoformat(), **data}
        for cb in self._subs:
            res = cb(event)
            if inspect.isawaitable(res):
                await res
