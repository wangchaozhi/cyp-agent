"""Alerter：把告警派发到多个 sink。sink 失败被隔离，不影响主流程。"""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any, Protocol

from cyp.config import Settings
from cyp.observability import get_logger, redact


class AlertSink(Protocol):
    async def emit(self, alert: dict) -> None: ...


class ConsoleSink:
    def __init__(self) -> None:
        self.log = get_logger("alert")

    async def emit(self, alert: dict) -> None:
        self.log.warning(alert.get("msg", "alert"), **{k: v for k, v in alert.items() if k != "msg"})


class WebhookSink:
    def __init__(self, url: str) -> None:
        self.url = url

    async def emit(self, alert: dict) -> None:
        import httpx
        async with httpx.AsyncClient(timeout=5) as c:
            await c.post(self.url, json=alert)


class Alerter:
    def __init__(self, sinks: list[AlertSink]) -> None:
        self.sinks = sinks
        self.log = get_logger("alert")

    async def alert(self, level: str, msg: str, **fields: Any) -> None:
        payload = {"level": level, "msg": msg, "ts": datetime.now(timezone.utc).isoformat(),
                   **redact(fields)}
        for sink in self.sinks:
            try:
                await sink.emit(payload)
            except Exception as e:  # noqa: BLE001 —— sink 失败隔离
                self.log.error("alert_sink_failed", error=str(e))


def build_alerter(settings: Settings) -> Alerter:
    sinks: list[AlertSink] = [ConsoleSink()]
    if settings.alert_webhook:
        sinks.append(WebhookSink(settings.alert_webhook))
    return Alerter(sinks)
