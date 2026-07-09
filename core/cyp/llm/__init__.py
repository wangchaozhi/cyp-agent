"""LLM 适配层：ResilientLLM + 可插拔 provider（anthropic / mock）。"""

from __future__ import annotations

from cyp.config import Settings
from cyp.llm.base import (
    LLMError,
    LLMMetrics,
    LLMProvider,
    ResilientLLM,
    TransientLLMError,
)
from cyp.llm.mock import MockProvider
from cyp.llm.openai_compatible import OpenAICompatibleProvider

__all__ = [
    "ResilientLLM",
    "LLMProvider",
    "LLMMetrics",
    "LLMError",
    "TransientLLMError",
    "MockProvider",
    "OpenAICompatibleProvider",
    "build_llm",
]


def build_llm(settings: Settings, metrics: LLMMetrics | None = None) -> ResilientLLM:
    """按配置构建真实 provider；缺 key 时 enabled=False（Agent 走规则降级）。"""
    if settings.llm_enabled:
        if settings.llm_provider == "deepseek":
            provider: LLMProvider = OpenAICompatibleProvider(
                api_key=settings.deepseek_api_key or "",
                base_url=settings.llm_base_url or "https://api.deepseek.com",
                default_model=settings.llm_model,
            )
        else:
            from cyp.llm.anthropic import AnthropicProvider
            provider = AnthropicProvider(settings.anthropic_api_key or "", settings.llm_model)
        enabled = True
    else:
        provider = MockProvider()
        enabled = False
    return ResilientLLM(
        provider, enabled=enabled, model=settings.llm_model,
        model_fast=settings.llm_model_fast, metrics=metrics,
    )
