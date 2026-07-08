"""ResilientLLM：重试 + 退避 + 熔断 + 超时 + 结构化输出，异常统一降级为 None。

设计要点（复用 prod-agent）：
- 只重试瞬态错误（TransientLLMError / 超时 / 连接错误），指数退避 + 抖动。
- 熔断器：连续失败达阈值后短路（一段冷却期直接返回 None，不再打后端）。
- json()：以 pydantic schema 约束结构化输出，校验失败也降级为 None。
- 所有失败路径返回 None，把「降级为规则模板」的决定权交给上层 Agent。
"""

from __future__ import annotations

import asyncio
import random
import time
from dataclasses import dataclass
from typing import Protocol, TypeVar

from pydantic import BaseModel, ValidationError

T = TypeVar("T", bound=BaseModel)


class LLMError(Exception):
    """不可重试的 LLM 错误。"""


class TransientLLMError(LLMError):
    """瞬态、可重试（429/5xx/超时等）。"""


class LLMProvider(Protocol):
    async def text(self, *, system: str, user: str, model: str | None = None) -> str: ...
    async def json(self, *, system: str, user: str, schema: dict, model: str | None = None) -> dict: ...


@dataclass
class LLMMetrics:
    calls: int = 0
    errors: int = 0
    retries: int = 0
    parse_errors: int = 0
    short_circuits: int = 0

    def snapshot(self) -> dict:
        return {"calls": self.calls, "errors": self.errors, "retries": self.retries,
                "parse_errors": self.parse_errors, "short_circuits": self.short_circuits}


class ResilientLLM:
    def __init__(
        self,
        provider: LLMProvider | None,
        enabled: bool,
        model: str = "",
        model_fast: str = "",
        max_retries: int = 2,
        base_delay: float = 0.2,
        timeout: float = 30.0,
        breaker_threshold: int = 4,
        breaker_cooldown: float = 15.0,
        metrics: LLMMetrics | None = None,
    ) -> None:
        self.provider = provider
        self.enabled = enabled and provider is not None
        self.model = model
        self.model_fast = model_fast
        self.max_retries = max_retries
        self.base_delay = base_delay
        self.timeout = timeout
        self._breaker_threshold = breaker_threshold
        self._breaker_cooldown = breaker_cooldown
        self.metrics = metrics or LLMMetrics()
        self._consecutive_failures = 0
        self._opened_at = 0.0

    # ---- 熔断 --------------------------------------------------------------

    def _breaker_open(self) -> bool:
        if self._consecutive_failures < self._breaker_threshold:
            return False
        if time.monotonic() - self._opened_at >= self._breaker_cooldown:
            self._consecutive_failures = 0   # 冷却结束，半开重试
            return False
        return True

    def _on_success(self) -> None:
        self._consecutive_failures = 0

    def _on_failure(self) -> None:
        self._consecutive_failures += 1
        if self._consecutive_failures == self._breaker_threshold:
            self._opened_at = time.monotonic()

    async def _call(self, coro_factory):
        if not self.enabled:
            return None
        if self._breaker_open():
            self.metrics.short_circuits += 1
            return None
        for attempt in range(self.max_retries + 1):
            try:
                result = await asyncio.wait_for(coro_factory(), timeout=self.timeout)
                self._on_success()
                self.metrics.calls += 1
                return result
            except Exception as e:  # noqa: BLE001 —— 统一降级
                self.metrics.errors += 1
                self._on_failure()
                transient = isinstance(e, (TransientLLMError, asyncio.TimeoutError, ConnectionError))
                if attempt < self.max_retries and transient:
                    self.metrics.retries += 1
                    delay = self.base_delay * (2 ** attempt) + random.uniform(0, self.base_delay)
                    await asyncio.sleep(delay)
                    continue
                return None
        return None

    # ---- 公开接口 ----------------------------------------------------------

    async def text(self, *, system: str, user: str, fast: bool = False) -> str | None:
        model = self.model_fast if fast else self.model
        return await self._call(lambda: self.provider.text(system=system, user=user, model=model))

    async def json(self, *, system: str, user: str, schema: type[T], fast: bool = False) -> T | None:
        model = self.model_fast if fast else self.model
        raw = await self._call(
            lambda: self.provider.json(system=system, user=user, schema=schema.model_json_schema(), model=model)
        )
        if raw is None:
            return None
        try:
            return schema.model_validate(raw)
        except ValidationError:
            self.metrics.parse_errors += 1
            return None
