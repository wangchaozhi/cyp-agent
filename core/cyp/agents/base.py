"""Agent 基座：依赖注入上下文 + 投票融合工具。

约定：每个 Agent 显式接收 AgentContext（llm/settings/lessons），不读全局单例；
LLM 调用一律"可 None"，None 即走规则降级——保证无密钥可端到端跑通。
"""

from __future__ import annotations

from dataclasses import dataclass, field

from cyp.config import Settings
from cyp.contracts import Signal
from cyp.llm import ResilientLLM


@dataclass
class AgentContext:
    llm: ResilientLLM
    settings: Settings
    lessons: list[str] = field(default_factory=list)   # 复盘经验回灌（长期记忆）


@dataclass
class Vote:
    sign: float   # -1..1（负=看空，正=看多）
    weight: float  # >0
    signal: Signal


def blend(votes: list[Vote]) -> tuple[str, float]:
    """加权融合多个投票 → (stance, confidence in [0,1])。"""
    tot = sum(v.weight for v in votes)
    if tot <= 0:
        return "neutral", 0.2
    net = sum(v.sign * v.weight for v in votes) / tot
    if net > 0.15:
        stance = "bullish"
    elif net < -0.15:
        stance = "bearish"
    else:
        stance = "neutral"
    return stance, min(1.0, abs(net))


def stance_sign(stance: str) -> float:
    return {"bullish": 1.0, "bearish": -1.0}.get(stance, 0.0)
