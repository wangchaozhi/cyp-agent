"""状态与检查点：崩溃可恢复 + 经验沉淀。

PostgreSQL 后端（DSN 取 CYP_DB_URL，默认 docker-compose 本地库），
lessons 带 symbol 元数据并支持相关性检索。
旧 JSON 存档首次打开自动迁移。
"""

from cyp.memory.store import MemoryStore

__all__ = ["MemoryStore"]
