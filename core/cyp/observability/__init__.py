"""可观测性：结构化 JSON 日志（自动脱敏）+ trace/span + 运行指标。

- 每轮一个 trace_id（= run_id），每步一个 span（时长/状态）。
- 日志自动脱敏 api_key/secret/private_key/token 等敏感字段。
- RunMetrics 汇总运行结果计数，供 GET /api/metrics 与巡检。
"""

from cyp.observability.logging import configure_logging, get_logger, redact
from cyp.observability.metrics import RunMetrics
from cyp.observability.tracing import Span, Trace

__all__ = ["configure_logging", "get_logger", "redact", "Trace", "Span", "RunMetrics"]
