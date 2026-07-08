"""结构化 JSON 日志 + 敏感字段脱敏。

零第三方依赖（stdlib logging）。任何进入日志的字段先过 redact()，
凡键名含 api_key/secret/private_key/mnemonic/token/password/authorization 即打码。
"""

from __future__ import annotations

import json
import logging
import sys
from datetime import datetime, timezone
from typing import Any

_REDACT_HINTS = ("api_key", "apikey", "api_secret", "secret", "private_key",
                 "mnemonic", "password", "token", "authorization")
_MASK = "***"


def _is_sensitive(key: str) -> bool:
    k = key.lower()
    return any(h in k for h in _REDACT_HINTS)


def redact(obj: Any) -> Any:
    """递归脱敏：敏感键的值替换为 ***。"""
    if isinstance(obj, dict):
        return {k: (_MASK if _is_sensitive(str(k)) else redact(v)) for k, v in obj.items()}
    if isinstance(obj, (list, tuple)):
        return [redact(v) for v in obj]
    return obj


class _JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        base = {
            "ts": datetime.now(timezone.utc).isoformat(),
            "level": record.levelname,
            "logger": record.name,
            "msg": record.getMessage(),
        }
        fields = getattr(record, "fields", None)
        if fields:
            base.update(redact(fields))
        return json.dumps(base, ensure_ascii=False, default=str)


class CypLogger:
    def __init__(self, logger: logging.Logger) -> None:
        self._l = logger

    def _log(self, level: int, msg: str, **fields: Any) -> None:
        self._l.log(level, msg, extra={"fields": fields})

    def debug(self, msg: str, **f: Any) -> None: self._log(logging.DEBUG, msg, **f)
    def info(self, msg: str, **f: Any) -> None: self._log(logging.INFO, msg, **f)
    def warning(self, msg: str, **f: Any) -> None: self._log(logging.WARNING, msg, **f)
    def error(self, msg: str, **f: Any) -> None: self._log(logging.ERROR, msg, **f)


def configure_logging(level: str = "INFO") -> None:
    """把 cyp 根 logger 接上 JSON handler（幂等）。CLI/服务启动时调用一次。"""
    root = logging.getLogger("cyp")
    root.setLevel(level.upper())
    if not any(getattr(h, "_cyp", False) for h in root.handlers):
        h = logging.StreamHandler(sys.stderr)
        h.setFormatter(_JsonFormatter())
        h._cyp = True  # type: ignore[attr-defined]
        root.addHandler(h)
    root.propagate = False


def get_logger(name: str) -> CypLogger:
    return CypLogger(logging.getLogger(f"cyp.{name}"))
