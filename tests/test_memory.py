"""MemoryStore（PostgreSQL）：检查点、经验上限、symbol 相关性检索、跨实例持久化。"""

from cyp.memory import MemoryStore


def test_checkpoint_roundtrip():
    m = MemoryStore()
    m.checkpoint("r1", "snapshot", {"bars": 100})
    m.checkpoint("r1", "proposal", {"side": "long"})
    cp = m.get_checkpoint("r1")
    assert cp["snapshot"] == {"bars": 100} and cp["proposal"]["side"] == "long"
    assert m.get_checkpoint("nope") == {}


def test_checkpoint_upsert_overwrites():
    m = MemoryStore()
    m.checkpoint("r1", "snapshot", {"bars": 100})
    m.checkpoint("r1", "snapshot", {"bars": 200})
    assert m.get_checkpoint("r1")["snapshot"] == {"bars": 200}


def test_lessons_capped_at_max():
    m = MemoryStore(max_lessons=5)
    m.append_lessons([f"lesson-{i}" for i in range(10)])
    got = m.get_lessons(100)
    assert len(got) == 5 and got[-1] == "lesson-9"


def test_lessons_relevance_prefers_same_symbol():
    m = MemoryStore()
    m.append_lessons(["BTC 滑点偏大注意限价"], symbol="BTC/USDT")
    m.append_lessons(["ETH 止损过近被扫"], symbol="ETH/USDT")
    m.append_lessons(["DOGE 流动性差"], symbol="DOGE/USDT")
    top = m.get_lessons(1, symbol="BTC/USDT")
    assert top == ["BTC 滑点偏大注意限价"]


def test_lessons_without_symbol_returns_recent():
    m = MemoryStore()
    m.append_lessons(["a"], symbol="X")
    m.append_lessons(["b"], symbol="Y")
    assert m.get_lessons(1) == ["b"]


def test_persistence_across_instances():
    m1 = MemoryStore()
    m1.append_lessons(["persist-me"], symbol="BTC/USDT")
    m1.close()
    m2 = MemoryStore()   # 新实例连同一库，数据仍在
    assert m2.get_lessons() == ["persist-me"]
