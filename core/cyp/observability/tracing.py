"""轻量 trace/span：每轮一个 Trace（trace_id=run_id），每步一个 span 记录时长与状态。"""

from __future__ import annotations

import time
from contextlib import asynccontextmanager
from dataclasses import dataclass, field


@dataclass
class Span:
    name: str
    start: float
    end: float | None = None
    status: str = "ok"
    error: str | None = None

    @property
    def duration_ms(self) -> float:
        return round(((self.end or time.monotonic()) - self.start) * 1000, 2)


@dataclass
class Trace:
    trace_id: str
    spans: list[Span] = field(default_factory=list)

    @asynccontextmanager
    async def span(self, name: str):
        s = Span(name=name, start=time.monotonic())
        self.spans.append(s)
        try:
            yield s
        except Exception as e:  # noqa: BLE001 —— 记录后原样抛出
            s.status = "error"
            s.error = str(e)
            raise
        finally:
            s.end = time.monotonic()

    def summary(self) -> dict:
        return {"trace_id": self.trace_id,
                "spans": [{"name": s.name, "ms": s.duration_ms, "status": s.status} for s in self.spans]}
