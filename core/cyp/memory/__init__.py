"""状态与检查点：崩溃可恢复 + 经验沉淀。

M0：内存 + 可选 JSON 落盘的轻量实现。
后续（见 ROADMAP）升级为 aiosqlite WAL 以获得任务租约与真正的断点续跑。
"""

from cyp.memory.store import MemoryStore

__all__ = ["MemoryStore"]
