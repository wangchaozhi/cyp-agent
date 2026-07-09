"""FastAPI 服务：REST 端点 + 完整 HTTP 审批闭环。用 httpx ASGI 传输离线跑。"""

import asyncio
import contextlib
import json
from datetime import datetime, timezone
from decimal import Decimal

import pytest

pytest.importorskip("fastapi")
import httpx
from cyp.config import Settings
from cyp.contracts import Candle, DerivativesData, MarketSnapshot, OrderIntent, SentimentData
from cyp.data import SyntheticMarketData
from cyp.venue import PaperVenue

from apps.server.main import create_app

run = asyncio.run


class UptrendData:
    async def snapshot(self, symbol: str) -> MarketSnapshot:
        candles = [Candle(ts=datetime.now(timezone.utc), open=Decimal(50000 + i * 125),
                          high=Decimal(50000 + i * 125 + 50), low=Decimal(50000 + i * 125 - 50),
                          close=Decimal(50000 + i * 125), volume=Decimal("100")) for i in range(80)]
        return MarketSnapshot(symbol=symbol, venue="synthetic", ohlcv=candles,
                              derivatives=DerivativesData(funding_rate=Decimal("-0.0005"),
                                                          long_short_ratio=Decimal("0.8")),
                              sentiment=SentimentData(fear_greed=20))


@contextlib.asynccontextmanager
async def _client(app):
    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://t") as c,
    ):
        yield c


def test_health_and_venues():
    async def scenario():
        app = create_app(Settings(_env_file=None))
        async with _client(app) as c:
            h = (await c.get("/api/health")).json()
            assert h["ok"] and h["mode"] == "paper"
            assert h["display_mode"] == "paper"
            s = (await c.get("/api/settings")).json()
            assert s["execution_venue"] == "paper"
            assert s["data_source"] == "synthetic"
            ids = {v["id"] for v in (await c.get("/api/venues")).json()}
            assert "paper" in ids and "binance" in ids
    run(scenario())


def test_settings_selects_okx_demo_runtime():
    async def scenario():
        settings = Settings(
            _env_file=None,
            execution_venue="okx",
            data_source="cex",
            cex_id="okx",
            okx_api_key="actual-okx-key",
            okx_api_secret="actual-okx-secret",
            okx_password="actual-okx-passphrase",
        )
        app = create_app(settings)
        async with _client(app) as c:
            h = (await c.get("/api/health")).json()
            assert h["display_mode"] == "OKX Demo"
            assert h["execution_venue"] == "okx"
            s = (await c.get("/api/settings")).json()
            assert s["execution_venue"] == "okx"
            assert s["data_source"] == "cex"
            assert s["cex_id"] == "okx"
            assert s["okx"] == {"configured": True, "demo": True}
    run(scenario())


def test_synthetic_live_ticks_move_latest_price():
    async def scenario():
        data = SyntheticMarketData(live_ticks=True)
        first = await data.snapshot("BTC/USDT")
        second = await data.snapshot("BTC/USDT")
        assert first.ohlcv[-1].close != second.ohlcv[-1].close
    run(scenario())


def test_paper_protective_orders_auto_close():
    async def scenario():
        venue = PaperVenue()
        venue.set_mark_price("BTC/USDT", Decimal("100"))
        result = await venue.place(OrderIntent(
            client_id="open-1",
            symbol="BTC/USDT",
            venue="paper",
            side="long",
            instrument="spot",
            size_quote=Decimal("1000"),
            stop_loss=Decimal("95"),
            take_profit=[Decimal("105")],
        ))
        assert result.status == "filled"
        assert len(await venue.positions()) == 1
        venue.set_mark_price("BTC/USDT", Decimal("106"))
        assert await venue.positions() == []
    run(scenario())


def test_settings_endpoint_is_redacted():
    async def scenario():
        settings = Settings(
            _env_file=None,
            llm_provider="deepseek",
            deepseek_api_key="actual-deepseek-key",
            llm_model="deepseek-chat",
            llm_model_fast="deepseek-chat",
            okx_api_key="actual-okx-key",
            okx_api_secret="actual-okx-secret",
            okx_password="actual-okx-passphrase",
            watchlist="BTC/USDT, ETH/USDT",
            approval="dashboard",
        )
        app = create_app(settings)
        async with _client(app) as c:
            r = (await c.get("/api/settings")).json()
            assert r["mode"] == "paper"
            assert r["approval"] == "dashboard"
            assert r["llm_enabled"] is True
            assert r["llm_provider"] == "deepseek"
            assert r["llm_model"] == "deepseek-chat"
            assert r["okx"] == {"configured": True, "demo": True}
            assert r["watchlist"] == ["BTC/USDT", "ETH/USDT"]
            assert "max_risk_per_trade" in r["risk"]
            assert r["live_guard"]["ok"] is True

            body = json.dumps(r, ensure_ascii=False)
            assert "actual-deepseek-key" not in body
            assert "actual-okx-key" not in body
            assert "actual-okx-secret" not in body
            assert "actual-okx-passphrase" not in body
    run(scenario())


def test_settings_endpoint_updates_llm_runtime():
    async def scenario():
        app = create_app(Settings(_env_file=None))
        async with _client(app) as c:
            r = (await c.post("/api/settings", json={
                "llm_provider": "deepseek",
                "llm_model": "deepseek-chat",
                "llm_model_fast": "deepseek-chat",
                "llm_base_url": "https://api.deepseek.com",
                "deepseek_api_key": "actual-deepseek-key",
            })).json()
            assert r["llm_enabled"] is True
            assert r["llm_provider"] == "deepseek"
            assert r["llm_model"] == "deepseek-chat"
            assert r["llm_base_url"] == "https://api.deepseek.com"
            assert (await c.get("/api/health")).json()["llm"] is True
            assert "actual-deepseek-key" not in json.dumps(r, ensure_ascii=False)
    run(scenario())


def test_killswitch_toggle():
    async def scenario():
        app = create_app(Settings(_env_file=None))
        async with _client(app) as c:
            assert (await c.get("/api/killswitch")).json()["kill"] is False
            await c.post("/api/killswitch", json={"on": True})
            assert (await c.get("/api/killswitch")).json()["kill"] is True
    run(scenario())


def test_risk_board_endpoint():
    async def scenario():
        app = create_app(Settings(_env_file=None))
        async with _client(app) as c:
            r = (await c.get("/api/risk")).json()
            assert r["mode"] == "paper" and r["kill"] is False
            assert set(r["drawdown"]) == {"daily", "weekly", "total"}
            assert r["live_guard"]["ok"] is True          # paper 恒通过
            assert "max_orders_per_hour" in r["limits"]
    run(scenario())


def test_portfolio_endpoint():
    async def scenario():
        app = create_app(Settings(_env_file=None))
        async with _client(app) as c:
            r = (await c.get("/api/portfolio")).json()
            assert set(r["clusters"]) == {"major", "alt"}
            assert "long" in r["clusters"]["major"] and "correlated_limit" in r
    run(scenario())


def test_backtest_endpoint():
    async def scenario():
        app = create_app(Settings(_env_file=None))
        async with _client(app) as c:
            r = (await c.post("/api/backtest", json={
                "symbol": "BTC/USDT",
                "bars": 120,
                "window": 30,
                "seed": 11,
                "drift": 0.001,
                "vol": 0.01,
            })).json()
            assert r["symbol"] == "BTC/USDT"
            assert r["n_bars"] == 120
            assert r["params"]["window"] == 30
            assert len(r["equity_curve"]) > 0
            assert {"total_return", "max_drawdown", "sharpe", "n_trades"} <= set(r["metrics"])

            bad = await c.post("/api/backtest", json={"bars": 80, "window": 80})
            assert bad.status_code == 422
    run(scenario())


def test_market_endpoint_offline_graceful():
    async def scenario():
        app = create_app(Settings(_env_file=None))
        async with _client(app) as c:
            r = (await c.get("/api/market?symbol=BTC/USDT")).json()
            assert r["symbol"] == "BTC/USDT" and "tickers" in r   # 离线无行情 → 空但不报错
    run(scenario())


def test_approval_404_when_no_pending():
    async def scenario():
        app = create_app(Settings(_env_file=None))
        async with _client(app) as c:
            r = await c.post("/api/approvals/nope", json={"decision": "approve"})
            assert r.status_code == 404
    run(scenario())


def test_full_http_approval_loop():
    async def scenario():
        venue = PaperVenue()
        app = create_app(Settings(_env_file=None), data_source=UptrendData(), venue=venue)
        async with _client(app) as c:
            run_id = (await c.post("/api/run", json={"symbol": "BTC/USDT"})).json()["run_id"]
            # 轮询直到出现待审批
            for _ in range(200):
                pending = (await c.get("/api/pending")).json()
                if any(p["run_id"] == run_id for p in pending):
                    break
                await asyncio.sleep(0.005)
            assert (await c.post(f"/api/approvals/{run_id}", json={"decision": "approve"})).json()["ok"]
            # 轮询直到成交建仓
            for _ in range(200):
                pos = (await c.get("/api/positions")).json()
                if len(pos) == 1:
                    break
                await asyncio.sleep(0.005)
            assert len(pos) == 1 and pos[0]["side"] == "long"
            assert "mark_price" in pos[0]
            assert "unrealized_pnl" in pos[0]
            assert "unrealized_pnl_pct" in pos[0]

            close = await c.post("/api/positions/close", json={
                "symbol": pos[0]["symbol"],
                "instrument": pos[0]["instrument"],
            })
            assert close.status_code == 200
            assert (await c.get("/api/positions")).json() == []
    run(scenario())
