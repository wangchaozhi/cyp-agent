"""OpenAI-compatible chat completions provider, including DeepSeek."""

from __future__ import annotations

import json
from typing import Any

import httpx

from cyp.llm.base import LLMError, TransientLLMError


class OpenAICompatibleProvider:
    def __init__(
        self,
        api_key: str,
        base_url: str,
        default_model: str,
        max_tokens: int = 2048,
        timeout: float = 30.0,
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self._api_key = api_key
        self._base_url = base_url.rstrip("/")
        self._default_model = default_model
        self._max_tokens = max_tokens
        self._timeout = timeout
        self._transport = transport
        self._client: httpx.AsyncClient | None = None

    def _cli(self) -> httpx.AsyncClient:
        if self._client is None:
            self._client = httpx.AsyncClient(
                base_url=self._base_url,
                headers={"Authorization": f"Bearer {self._api_key}"},
                timeout=self._timeout,
                transport=self._transport,
            )
        return self._client

    def _map_error(self, error: Exception) -> Exception:
        if isinstance(error, (httpx.ConnectError, httpx.TimeoutException, httpx.NetworkError)):
            return TransientLLMError(str(error))
        if isinstance(error, httpx.HTTPStatusError):
            if error.response.status_code == 429 or error.response.status_code >= 500:
                return TransientLLMError(str(error))
            return LLMError(str(error))
        return error

    async def _chat(
        self,
        *,
        system: str,
        user: str,
        model: str | None = None,
        response_format: dict[str, str] | None = None,
    ) -> str:
        payload: dict[str, Any] = {
            "model": model or self._default_model,
            "messages": [{"role": "system", "content": system}, {"role": "user", "content": user}],
            "max_tokens": self._max_tokens,
        }
        if response_format is not None:
            payload["response_format"] = response_format

        try:
            resp = await self._cli().post("/chat/completions", json=payload)
            resp.raise_for_status()
            data = resp.json()
        except Exception as e:  # noqa: BLE001
            raise self._map_error(e) from e

        try:
            return data["choices"][0]["message"]["content"] or ""
        except (KeyError, IndexError, TypeError) as e:
            raise TransientLLMError("OpenAI-compatible 响应缺少 message.content") from e

    async def text(self, *, system: str, user: str, model: str | None = None) -> str:
        return await self._chat(system=system, user=user, model=model)

    async def json(self, *, system: str, user: str, schema: dict, model: str | None = None) -> dict:
        schema_text = json.dumps(schema, ensure_ascii=False)
        content = await self._chat(
            system=(
                f"{system}\n\n"
                "你必须只返回一个 JSON object，不要 Markdown，不要解释。"
                f"JSON 必须符合这个 schema：{schema_text}"
            ),
            user=user,
            model=model,
            response_format={"type": "json_object"},
        )
        try:
            return json.loads(content)
        except json.JSONDecodeError as e:
            raise TransientLLMError("模型未返回合法 JSON") from e
