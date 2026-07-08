"""MockProvider：确定性、零密钥。

- 默认：text 返回占位串，json 抛错（模拟"无有效结构化输出"→ 上层降级）。
- 测试可注入 text_fn / json_fn 定制确定性行为，或用 fail_times 模拟瞬态失败以测重试/熔断。
"""

from __future__ import annotations

from collections.abc import Callable

from cyp.llm.base import TransientLLMError


class MockProvider:
    def __init__(
        self,
        text_fn: Callable[[str, str], str] | None = None,
        json_fn: Callable[[str, str, dict], dict] | None = None,
        fail_times: int = 0,
        transient: bool = True,
    ) -> None:
        self._text_fn = text_fn
        self._json_fn = json_fn
        self._fail_times = fail_times
        self._transient = transient
        self.calls = 0

    def _maybe_fail(self) -> None:
        self.calls += 1
        if self.calls <= self._fail_times:
            raise TransientLLMError("mock transient") if self._transient else RuntimeError("mock fatal")

    async def text(self, *, system: str, user: str, model: str | None = None) -> str:
        self._maybe_fail()
        if self._text_fn:
            return self._text_fn(system, user)
        return "[mock] 无 LLM 密钥，返回占位文本。"

    async def json(self, *, system: str, user: str, schema: dict, model: str | None = None) -> dict:
        self._maybe_fail()
        if self._json_fn:
            return self._json_fn(system, user, schema)
        raise TransientLLMError("mock 无结构化输出（触发上层规则降级）")
