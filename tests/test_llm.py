"""ResilientLLM：禁用降级 / 结构化校验 / 重试 / 熔断。全部离线。"""

import asyncio
import json

import httpx
from cyp.config import Settings
from cyp.llm import MockProvider, ResilientLLM
from cyp.llm.openai_compatible import OpenAICompatibleProvider
from pydantic import BaseModel


def run(coro):
    return asyncio.run(coro)


class Demo(BaseModel):
    stance: str
    confidence: float


def test_disabled_returns_none():
    llm = ResilientLLM(MockProvider(), enabled=False)
    assert run(llm.json(system="s", user="u", schema=Demo)) is None
    assert run(llm.text(system="s", user="u")) is None


def test_text_uses_provider():
    llm = ResilientLLM(MockProvider(text_fn=lambda s, u: "hello"), enabled=True)
    assert run(llm.text(system="s", user="u")) == "hello"


def test_json_valid_parsed():
    p = MockProvider(json_fn=lambda s, u, sch: {"stance": "bullish", "confidence": 0.6})
    llm = ResilientLLM(p, enabled=True)
    out = run(llm.json(system="s", user="u", schema=Demo))
    assert isinstance(out, Demo) and out.stance == "bullish"


def test_json_invalid_degrades_to_none():
    p = MockProvider(json_fn=lambda s, u, sch: {"confidence": 0.6})  # 缺 stance
    llm = ResilientLLM(p, enabled=True)
    out = run(llm.json(system="s", user="u", schema=Demo))
    assert out is None
    assert llm.metrics.parse_errors == 1


def test_retry_then_success():
    # 前 1 次瞬态失败，之后成功 → 应重试并最终返回
    p = MockProvider(json_fn=lambda s, u, sch: {"stance": "neutral", "confidence": 0.5}, fail_times=1)
    llm = ResilientLLM(p, enabled=True, max_retries=2, base_delay=0.0)
    out = run(llm.json(system="s", user="u", schema=Demo))
    assert isinstance(out, Demo)
    assert llm.metrics.retries >= 1


def test_circuit_breaker_short_circuits():
    p = MockProvider(json_fn=lambda s, u, sch: {"stance": "x", "confidence": 0.1}, fail_times=10**9)
    llm = ResilientLLM(p, enabled=True, max_retries=0, breaker_threshold=2, breaker_cooldown=100)
    run(llm.json(system="s", user="u", schema=Demo))   # 失败1
    run(llm.json(system="s", user="u", schema=Demo))   # 失败2 → 打开熔断
    run(llm.json(system="s", user="u", schema=Demo))   # 短路
    assert llm.metrics.short_circuits >= 1


def test_deepseek_settings_enable_llm():
    settings = Settings(_env_file=None, llm_provider="deepseek", deepseek_api_key="sk-test")
    assert settings.llm_enabled is True


def test_openai_compatible_json_provider():
    async def handler(request: httpx.Request) -> httpx.Response:
        payload = json.loads(request.content)
        assert payload["model"] == "deepseek-chat"
        assert payload["response_format"] == {"type": "json_object"}
        return httpx.Response(
            200,
            json={
                "choices": [
                    {"message": {"content": json.dumps({"stance": "neutral", "confidence": 0.7})}}
                ]
            },
        )

    provider = OpenAICompatibleProvider(
        api_key="sk-test",
        base_url="https://api.deepseek.com",
        default_model="deepseek-chat",
        transport=httpx.MockTransport(handler),
    )
    llm = ResilientLLM(provider, enabled=True, model="deepseek-chat")
    out = run(llm.json(system="s", user="u", schema=Demo))
    assert isinstance(out, Demo)
    assert out.stance == "neutral"
