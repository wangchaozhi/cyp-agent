"""FastAPI 服务：REST + SSE，把编排器暴露给仪表盘。

    uvicorn apps.server.main:app --reload

端点：
  POST /api/run            触发一轮闭环（后台任务），返回 run_id
  GET  /api/events         SSE 事件流（编排器每步推送）
  GET  /api/pending        待审批列表
  POST /api/approvals/{id} 批准/拒绝/修改（人在环）
  GET  /api/positions      当前持仓
  GET  /api/venues         场所能力与配置
  GET/POST /api/killswitch  查询/切换 Kill Switch
  GET  /                   仪表盘页面
"""

from __future__ import annotations

import asyncio
import json
import uuid
from pathlib import Path

from fastapi import FastAPI, HTTPException
from fastapi.responses import HTMLResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel, Field

from cyp.approval import PendingApprovalGate
from cyp.backtest import Backtester
from cyp.config import Settings, get_settings
from cyp.data import SyntheticMarketData
from cyp.events import EventBus
from cyp.orchestrator import Orchestrator
from cyp.venue import MarketAggregator, PaperVenue, build_registry

_WEB_DIR = Path(__file__).resolve().parents[1] / "web"
_WEB_DIST = _WEB_DIR / "dist"


class RunRequest(BaseModel):
    symbol: str | None = None


class ApprovalRequest(BaseModel):
    decision: str
    size: float | None = None
    note: str = ""


class KillRequest(BaseModel):
    on: bool


class BacktestRequest(BaseModel):
    symbol: str | None = None
    bars: int = Field(default=300, ge=80, le=5000)
    window: int = Field(default=60, ge=20, le=1000)
    seed: int = Field(default=7, ge=0, le=1_000_000)
    drift: float = Field(default=0.001, ge=-0.05, le=0.05)
    vol: float = Field(default=0.01, gt=0, le=0.2)


def create_app(settings: Settings | None = None, data_source=None, venue=None) -> FastAPI:
    settings = settings or get_settings()
    events = EventBus()
    gate = PendingApprovalGate(timeout=settings.risk.approval_timeout_seconds, events=events)
    venue = venue or PaperVenue()
    data = data_source or SyntheticMarketData()
    registry = build_registry(settings)
    others = [v for v in registry.all() if getattr(v, "id", None) != getattr(venue, "id", None)]
    aggregator = MarketAggregator([v for v in registry.all() if getattr(v, "kind", None) == "cex"])
    orch = Orchestrator(settings=settings, data_source=data, venue=venue, events=events,
                        approval=gate, risk_venues=[venue, *others])

    app = FastAPI(title="cyp-agent", version="0.1.0")
    app.state.settings = settings
    app.state.orch = orch
    app.state.gate = gate
    app.state.venue = venue
    app.state.registry = registry
    app.state.subscribers = []   # list[asyncio.Queue]
    app.state.tasks = set()

    assets_dir = _WEB_DIST / "assets"
    if assets_dir.exists():
        app.mount("/assets", StaticFiles(directory=assets_dir), name="web-assets")

    # 事件 → 广播到所有 SSE 客户端队列
    def _broadcast(evt: dict) -> None:
        for q in list(app.state.subscribers):
            try:
                q.put_nowait(evt)
            except asyncio.QueueFull:
                pass
    events.subscribe(_broadcast)

    @app.get("/api/health")
    async def health():
        return {"ok": True, "mode": settings.mode, "llm": settings.llm_enabled, "kill": settings.kill}

    @app.get("/api/venues")
    async def venues():
        return registry.describe()

    @app.get("/api/market")
    async def market(symbol: str | None = None):
        sym = symbol or settings.watchlist_symbols()[0]
        tickers = await aggregator.tickers(sym)
        buy_v, buy_p = await aggregator.best_venue(sym, "long")
        sell_v, sell_p = await aggregator.best_venue(sym, "short")
        spread = await aggregator.spread_bps(sym)
        return {"symbol": sym, "tickers": {k: str(v) for k, v in tickers.items()},
                "best_buy": {"venue": buy_v, "price": str(buy_p) if buy_p else None},
                "best_sell": {"venue": sell_v, "price": str(sell_p) if sell_p else None},
                "spread_bps": str(spread) if spread is not None else None}

    @app.get("/api/positions")
    async def positions():
        return [p.model_dump(mode="json") for p in await venue.positions()]

    @app.get("/api/metrics")
    async def metrics():
        return {"runs": orch.metrics.snapshot(), "llm": orch.llm.metrics.snapshot()}

    @app.get("/api/risk")
    async def risk():
        from cyp.live import LiveGuard
        bal = await venue.balances()
        equity = bal.total_quote if bal.total_quote > 0 else bal.free_quote
        snap = orch.portfolio.risk_snapshot(equity)
        rc = settings.risk
        guard = LiveGuard.check(settings)
        return {
            "mode": settings.mode, "kill": settings.kill, "equity": str(equity),
            "drawdown": {k: str(snap[f"{k}_drawdown"]) for k in ("daily", "weekly", "total")},
            "orders_last_hour": snap["orders_last_hour"],
            "consecutive_losses": snap["consecutive_losses"],
            "limits": {"daily_dd": str(rc.daily_drawdown_limit), "weekly_dd": str(rc.weekly_drawdown_limit),
                       "total_dd": str(rc.max_drawdown_limit), "max_leverage": str(rc.max_leverage),
                       "max_orders_per_hour": rc.max_orders_per_hour,
                       "max_consecutive_losses": rc.max_consecutive_losses},
            "live_guard": {"ok": guard.ok, "reasons": guard.reasons},
        }

    @app.get("/api/pending")
    async def pending():
        return gate.list_pending()

    @app.get("/api/portfolio")
    async def portfolio():
        from cyp.portfolio import CorrelationModel, PortfolioView, aggregate_positions
        positions = await aggregate_positions(orch.risk_venues)
        view = PortfolioView(positions, CorrelationModel())
        bal = await venue.balances()
        equity = bal.total_quote if bal.total_quote > 0 else bal.free_quote
        clusters = {cl: {"long": str(view.cluster_net_directional(cl, "long")),
                         "short": str(view.cluster_net_directional(cl, "short"))}
                    for cl in ("major", "alt")}
        return {"equity": str(equity), "n_positions": len(positions),
                "gross": str(view.gross_notional()), "clusters": clusters,
                "correlated_limit": str(equity * settings.risk.max_correlated_exposure)}

    @app.post("/api/backtest")
    async def backtest(req: BacktestRequest):
        if req.window >= req.bars:
            raise HTTPException(status_code=422, detail="window must be smaller than bars")

        symbol = req.symbol or settings.watchlist_symbols()[0]
        market = SyntheticMarketData(bars=req.bars, seed=req.seed, drift=req.drift, vol=req.vol)
        candles = (await market.snapshot(symbol)).ohlcv
        backtest_settings = settings.model_copy(update={"mode": "paper", "kill": False, "approval": "auto"})
        report = await Backtester(backtest_settings, symbol, candles, window=req.window).run()
        payload = report.model_dump(mode="json")
        payload["params"] = {
            "symbol": symbol,
            "bars": req.bars,
            "window": req.window,
            "seed": req.seed,
            "drift": req.drift,
            "vol": req.vol,
        }
        return payload

    @app.post("/api/run")
    async def run(req: RunRequest):
        symbol = req.symbol or settings.watchlist_symbols()[0]
        run_id = uuid.uuid4().hex[:12]
        task = asyncio.create_task(orch.run_once(symbol, run_id))
        app.state.tasks.add(task)
        task.add_done_callback(app.state.tasks.discard)
        return {"run_id": run_id, "symbol": symbol}

    @app.post("/api/approvals/{run_id}")
    async def approve(run_id: str, req: ApprovalRequest):
        ok = gate.resolve(run_id, req.decision, size=req.size, note=req.note)
        if not ok:
            raise HTTPException(status_code=404, detail="无此待审批项或已处理")
        return {"ok": True}

    @app.get("/api/killswitch")
    async def killswitch_get():
        return {"kill": settings.kill}

    @app.post("/api/killswitch")
    async def killswitch_set(req: KillRequest):
        settings.kill = req.on
        await events.publish("killswitch", "-", on=req.on)
        return {"kill": settings.kill}

    @app.get("/api/events")
    async def events_stream():
        queue: asyncio.Queue = asyncio.Queue(maxsize=1000)
        app.state.subscribers.append(queue)

        async def gen():
            try:
                yield "retry: 3000\n\n"
                while True:
                    try:
                        evt = await asyncio.wait_for(queue.get(), timeout=15)
                        yield f"data: {json.dumps(evt, ensure_ascii=False)}\n\n"
                    except asyncio.TimeoutError:
                        yield ": keepalive\n\n"
            finally:
                if queue in app.state.subscribers:
                    app.state.subscribers.remove(queue)

        return StreamingResponse(gen(), media_type="text/event-stream")

    @app.get("/", response_class=HTMLResponse)
    async def index():
        html = _WEB_DIST / "index.html"
        if html.exists():
            return HTMLResponse(html.read_text(encoding="utf-8"))
        return HTMLResponse(
            "<h1>cyp-agent</h1>"
            "<p>React 仪表盘尚未构建。</p>"
            "<p>开发：cd apps/web && npm install && npm run dev</p>"
            "<p>同源部署：cd apps/web && npm install && npm run build，然后启动 FastAPI。</p>"
        )

    return app


app = create_app()
