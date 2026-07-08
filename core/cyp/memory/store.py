"""MemoryStore：检查点 + 经验条目。M0 内存实现（可选 JSON 落盘）。

接口刻意与未来 aiosqlite 版对齐：checkpoint/get_checkpoint/append_lessons/get_lessons，
届时只替换后端不动上层。
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any


class MemoryStore:
    def __init__(self, path: str | None = None, max_lessons: int = 200) -> None:
        self._path = Path(path) if path else None
        self._max_lessons = max_lessons
        self._checkpoints: dict[str, dict[str, Any]] = {}
        self._lessons: list[str] = []
        if self._path and self._path.exists():
            self._load()

    def checkpoint(self, run_id: str, step: str, data: dict[str, Any]) -> None:
        self._checkpoints.setdefault(run_id, {})[step] = data
        self._persist()

    def get_checkpoint(self, run_id: str) -> dict[str, Any]:
        return self._checkpoints.get(run_id, {})

    def append_lessons(self, lessons: list[str]) -> None:
        for l in lessons:
            if l:
                self._lessons.append(l)
        self._lessons = self._lessons[-self._max_lessons:]
        self._persist()

    def get_lessons(self, n: int = 20) -> list[str]:
        return self._lessons[-n:]

    # ---- 落盘（可选）------------------------------------------------------

    def _persist(self) -> None:
        if not self._path:
            return
        self._path.parent.mkdir(parents=True, exist_ok=True)
        self._path.write_text(json.dumps(
            {"checkpoints": self._checkpoints, "lessons": self._lessons},
            ensure_ascii=False, default=str), encoding="utf-8")

    def _load(self) -> None:
        raw = json.loads(self._path.read_text(encoding="utf-8"))
        self._checkpoints = raw.get("checkpoints", {})
        self._lessons = raw.get("lessons", [])
