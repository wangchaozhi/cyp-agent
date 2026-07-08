"""告警：下单失败/熔断/异常 → 控制台 + 可选 webhook。字段自动脱敏。"""

from cyp.alerts.alerter import Alerter, ConsoleSink, WebhookSink, build_alerter

__all__ = ["Alerter", "ConsoleSink", "WebhookSink", "build_alerter"]
