"""MemoryStore：检查点 + 经验条目。SQLite 后端（M6），path=None 时纯内存库。

- lessons 带 symbol 元数据，`get_lessons(symbol=...)` 按「符号匹配 + 词元重合度」
  打分检索最相关条目（轻量长期记忆，不引向量库）。
- 兼容旧接口：checkpoint/get_checkpoint/append_lessons/get_lessons 签名不变，
  旧 JSON 落盘文件会在首次打开时自动迁移进 SQLite。
"""

from __future__ import annotations

import json
import re
import sqlite3
from pathlib import Path
from typing import Any

_SCHEMA = """
CREATE TABLE IF NOT EXISTS lessons (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
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


def _tokens(text: str) -> set[str]:
    return {t.lower() for t in re.findall(r"[A-Za-z0-9\u4e00-\u9fff]+", text) if len(t) > 1}


class MemoryStore:
    def __init__(self, path: str | None = None, max_lessons: int = 200) -> None:
        self._max_lessons = max_lessons
        legacy_json: dict | None = None
        if path:
            p = Path(path)
            p.parent.mkdir(parents=True, exist_ok=True)
            if p.exists() and p.suffix == ".json":   # 旧 JSON 存档 → 迁移进 SQLite（同名 .sqlite）
                legacy_json = json.loads(p.read_text(encoding="utf-8"))
                p = p.with_suffix(".sqlite")
            self._conn = sqlite3.connect(str(p))
        else:
            self._conn = sqlite3.connect(":memory:")
        self._conn.executescript(_SCHEMA)
        if legacy_json:
            self._migrate(legacy_json)

    def _migrate(self, raw: dict) -> None:
        for lesson in raw.get("lessons", []):
            self._conn.execute("INSERT INTO lessons (symbol, text) VALUES ('', ?)", (lesson,))
        for run_id, steps in raw.get("checkpoints", {}).items():
            for step, data in steps.items():
                self._conn.execute(
                    "INSERT OR REPLACE INTO checkpoints (run_id, step, data) VALUES (?, ?, ?)",
                    (run_id, step, json.dumps(data, ensure_ascii=False, default=str)))
        self._conn.commit()

    # ---- 检查点 ------------------------------------------------------------

    def checkpoint(self, run_id: str, step: str, data: dict[str, Any]) -> None:
        self._conn.execute(
            "INSERT OR REPLACE INTO checkpoints (run_id, step, data) VALUES (?, ?, ?)",
            (run_id, step, json.dumps(data, ensure_ascii=False, default=str)))
        self._conn.commit()

    def get_checkpoint(self, run_id: str) -> dict[str, Any]:
        rows = self._conn.execute(
            "SELECT step, data FROM checkpoints WHERE run_id=?", (run_id,)).fetchall()
        return {step: json.loads(data) for step, data in rows}

    # ---- 经验条目 ----------------------------------------------------------

    def append_lessons(self, lessons: list[str], symbol: str = "") -> None:
        for lesson in lessons:
            if lesson:
                self._conn.execute("INSERT INTO lessons (symbol, text) VALUES (?, ?)",
                                   (symbol, lesson))
        # 只保留最近 max_lessons 条
        self._conn.execute(
            "DELETE FROM lessons WHERE id NOT IN (SELECT id FROM lessons ORDER BY id DESC LIMIT ?)",
            (self._max_lessons,))
        self._conn.commit()

    def get_lessons(self, n: int = 20, symbol: str | None = None) -> list[str]:
        """无 symbol：最近 n 条；有 symbol：按「符号匹配 + 词元重合」打分取最相关 n 条。"""
        rows = self._conn.execute(
            "SELECT id, symbol, text FROM lessons ORDER BY id DESC LIMIT ?",
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
        self._conn.close()
