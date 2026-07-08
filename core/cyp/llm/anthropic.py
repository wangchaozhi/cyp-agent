"""AnthropicProvider：官方 SDK。结构化输出走 tool-use（强制模型填 schema）。

惰性导入 anthropic；把 429/5xx/超时等映射为 TransientLLMError 让 ResilientLLM 重试。
API Key 只从 env 注入（config 层），此处不落盘、不打日志。
"""

from __future__ import annotations

from cyp.llm.base import LLMError, TransientLLMError


class AnthropicProvider:
    def __init__(self, api_key: str, default_model: str = "claude-opus-4-8", max_tokens: int = 2048) -> None:
        self._api_key = api_key
        self._default_model = default_model
        self._max_tokens = max_tokens
        self._client = None

    def _cli(self):
        if self._client is None:
            try:
                import anthropic
            except ImportError as e:  # pragma: no cover
                raise LLMError("需要 anthropic：pip install anthropic") from e
            self._client = anthropic.AsyncAnthropic(api_key=self._api_key)
        return self._client

    def _map_error(self, e: Exception) -> Exception:
        try:
            import anthropic
        except ImportError:  # pragma: no cover
            return e
        if isinstance(e, (anthropic.RateLimitError, anthropic.APITimeoutError, anthropic.InternalServerError,
                          anthropic.APIConnectionError)):
            return TransientLLMError(str(e))
        if isinstance(e, anthropic.APIStatusError) and e.status_code >= 500:
            return TransientLLMError(str(e))
        return e

    async def text(self, *, system: str, user: str, model: str | None = None) -> str:
        try:
            resp = await self._cli().messages.create(
                model=model or self._default_model, max_tokens=self._max_tokens,
                system=system, messages=[{"role": "user", "content": user}],
            )
        except Exception as e:  # noqa: BLE001
            raise self._map_error(e) from e
        return "".join(b.text for b in resp.content if getattr(b, "type", None) == "text")

    async def json(self, *, system: str, user: str, schema: dict, model: str | None = None) -> dict:
        tool = {"name": "emit", "description": "以给定 schema 返回结构化结果", "input_schema": schema}
        try:
            resp = await self._cli().messages.create(
                model=model or self._default_model, max_tokens=self._max_tokens,
                system=system, messages=[{"role": "user", "content": user}],
                tools=[tool], tool_choice={"type": "tool", "name": "emit"},
            )
        except Exception as e:  # noqa: BLE001
            raise self._map_error(e) from e
        for block in resp.content:
            if getattr(block, "type", None) == "tool_use" and block.name == "emit":
                return block.input
        raise TransientLLMError("模型未返回 tool_use 结构化输出")
