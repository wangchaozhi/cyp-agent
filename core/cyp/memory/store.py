"""MemoryStore：检查点 + 经验条目。PostgreSQL 后端（docker-compose 本地库）。

- lessons 带 symbol 元数据，`get_lessons(symbol=...)` 按「符号匹配 + 词元重合度」
  打分检索最相关条目（轻量长期记忆，不引向量库）。
- 同步接口（psycopg 同步连接，按操作短连接，无跨事件循环/线程共享问题）；
  DSN 缺省取 `CYP_DB_URL`（默认指向 docker-compose 里的本地 PG）。
"""

from __future__ import annotations

import json
import os
import re
from typing import Any

import psycopg

DEFAULT_DB_URL = "postgresql://cyp:cyp@localhost:5433/cyp"

_SCHEMA = """
CREATE TABLE IF NOT EXISTS lessons (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    symbol TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS checkpoints (
    run_id TEXT NOT NULL,
    step TEXT NOT NULL,
    data TEXT NOT NULL,
    PRIMARY KEY (run_id, step)
);
"""

_initialized_dsns: set[str] = set()   # 每进程每 DSN 只建一次表


def default_db_url() -> str:
    return os.environ.get("CYP_DB_URL", DEFAULT_DB_URL)


def _tokens(text: str) -> set[str]:
    return {t.lower() for t in re.findall(r"[A-Za-z0-9\u4e00-\u9fff]+", text) if len(t) > 1}


class MemoryStore:
    def __init__(self, dsn: str | None = None, max_lessons: int = 200) -> None:
        self._dsn = dsn or default_db_url()
        self._max_lessons = max_lessons
        if self._dsn not in _initialized_dsns:
            with self._connect() as conn:
                conn.execute(_SCHEMA)
            _initialized_dsns.add(self._dsn)

    def _connect(self) -> psycopg.Connection:
        return psycopg.connect(self._dsn)

    # ---- 检查点 ------------------------------------------------------------

    def checkpoint(self, run_id: str, step: str, data: dict[str, Any]) -> None:
        with self._connect() as conn:
            conn.execute(
                "INSERT INTO checkpoints (run_id, step, data) VALUES (%s, %s, %s) "
                "ON CONFLICT (run_id, step) DO UPDATE SET data = EXCLUDED.data",
                (run_id, step, json.dumps(data, ensure_ascii=False, default=str)))

    def get_checkpoint(self, run_id: str) -> dict[str, Any]:
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT step, data FROM checkpoints WHERE run_id=%s", (run_id,)).fetchall()
        return {step: json.loads(data) for step, data in rows}

    # ---- 经验条目 ----------------------------------------------------------

    def append_lessons(self, lessons: list[str], symbol: str = "") -> None:
        with self._connect() as conn:
            for lesson in lessons:
                if lesson:
                    conn.execute("INSERT INTO lessons (symbol, text) VALUES (%s, %s)",
                                 (symbol, lesson))
            # 只保留最近 max_lessons 条
            conn.execute(
                "DELETE FROM lessons WHERE id NOT IN "
                "(SELECT id FROM lessons ORDER BY id DESC LIMIT %s)",
                (self._max_lessons,))

    def get_lessons(self, n: int = 20, symbol: str | None = None) -> list[str]:
        """无 symbol：最近 n 条；有 symbol：按「符号匹配 + 词元重合」打分取最相关 n 条。"""
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT id, symbol, text FROM lessons ORDER BY id DESC LIMIT %s",
                (self._max_lessons,)).fetchall()
        rows.reverse()   # 按时间升序
        if not symbol:
            return [text for _, _, text in rows[-n:]]

        query_tokens = _tokens(symbol)
        scored = []
        for idx, (rid, sym, text) in enumerate(rows):
            score = 0.0
            if sym and sym == symbol:
                score += 2.0
            overlap = query_tokens & _tokens(f"{sym} {text}")
            score += len(overlap) * 0.5
            score += idx * 1e-6      # 同分时偏向更近的经验
            scored.append((score, rid, text))
        scored.sort(key=lambda t: t[0], reverse=True)
        top = scored[:n]
        top.sort(key=lambda t: t[1])   # 还原时间序，便于阅读
        return [text for _, _, text in top]

    def close(self) -> None:
        """接口兼容保留：连接按操作即用即关，无常驻资源。"""
