"""测试共用：持久化全部走本地 docker PG（TimescaleDB，见 docker-compose.yml）。

- pytest 启动时基于 CYP_DB_URL（默认 docker-compose 的 cyp 库）创建独立测试库
  cyp_test，并把 CYP_DB_URL 指向它——MemoryStore/OhlcvArchive/Settings 的默认
  构造全部落到测试库，不污染开发数据。
- 每个测试结束后清空持久化表，保证测试相互独立。
"""

from __future__ import annotations

import os
from urllib.parse import urlsplit, urlunsplit

import psycopg
import pytest

_BASE = os.environ.get("CYP_DB_URL", "postgresql://cyp:cyp@localhost:5433/cyp")
_TEST_DB = "cyp_test"


def _with_dbname(dsn: str, dbname: str) -> str:
    parts = urlsplit(dsn)
    return urlunsplit((parts.scheme, parts.netloc, f"/{dbname}", parts.query, parts.fragment))


TEST_DSN = _with_dbname(_BASE, _TEST_DB)


def pytest_configure(config):
    try:
        with psycopg.connect(_BASE, autocommit=True, connect_timeout=5) as conn:
            row = conn.execute("SELECT 1 FROM pg_database WHERE datname=%s",
                               (_TEST_DB,)).fetchone()
            if not row:
                conn.execute(f'CREATE DATABASE "{_TEST_DB}"')
    except psycopg.OperationalError as e:
        pytest.exit(f"测试需要本地 PostgreSQL（先 docker compose up -d）：{e}", returncode=4)
    os.environ["CYP_DB_URL"] = TEST_DSN


@pytest.fixture(autouse=True)
def _clean_tables():
    yield
    with psycopg.connect(TEST_DSN, autocommit=True) as conn:
        for table in ("lessons", "checkpoints", "ohlcv"):
            if conn.execute("SELECT to_regclass(%s)", (table,)).fetchone()[0]:
                conn.execute(f'TRUNCATE TABLE "{table}"')
