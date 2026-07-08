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

__all__ = [
    "ResilientLLM",
    "LLMProvider",
    "LLMMetrics",
    "LLMError",
    "TransientLLMError",
    "MockProvider",
    "build_llm",
]


def build_llm(settings: Settings, metrics: LLMMetrics | None = None) -> ResilientLLM:
    """有 ANTHROPIC_API_KEY → 真实 provider；否则 enabled=False（Agent 走规则降级）。"""
    if settings.llm_enabled:
        from cyp.llm.anthropic import AnthropicProvider
        provider: LLMProvider = AnthropicProvider(settings.anthropic_api_key, settings.llm_model)
        enabled = True
    else:
        provider = MockProvider()
        enabled = False
    return ResilientLLM(
        provider, enabled=enabled, model=settings.llm_model,
        model_fast=settings.llm_model_fast, metrics=metrics,
    )
