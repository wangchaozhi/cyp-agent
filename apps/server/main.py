"""FastAPI 服务：REST + SSE，把编排器暴露给仪表盘。

    uvicorn apps.server.main:app --reload

端点：
  POST /api/run            触发一轮闭环（后台任务），返回 run_id
  GET  /api/events         SSE 事件流（编排器每步推送）
  GET  /api/pending        待审批列表
  POST /api/approvals/{id} 批准/拒绝/修改（人在环）
  GET  /api/positions      当前持仓
  GET  /api/venues         场所能力与配置
  GET  /api/settings       运行配置脱敏快照
  GET/POST /api/killswitch  查询/切换 Kill Switch
  GET  /                   仪表盘页面
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import uuid
from decimal import Decimal
from pathlib import Path

from cyp.approval import PendingApprovalGate, wrap_with_policy
from cyp.backtest import Backtester
from cyp.config import Settings, get_settings
from cyp.contracts import OrderIntent
from cyp.data import CexMarketData, SyntheticMarketData
from cyp.events import EventBus
from cyp.llm import build_llm
from cyp.orchestrator import Orchestrator
from cyp.venue import MarketAggregator, build_registry
from fastapi import FastAPI, HTTPException
from fastapi.responses import HTMLResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel, Field

_WEB_DIR = Path(__file__).resolve().parents[1] / "web"
_WEB_DIST = _WEB_DIR / "dist"


class RunRequest(BaseModel):
    symbol: str | None = None


class ApprovalRequest(BaseModel):
    decision: str
    size: float | None = None
    note: str = ""
    operator: str = ""      # 多操作员：审批人身份记入审计


class KillRequest(BaseModel):
    on: bool


class ClosePositionRequest(BaseModel):
    symbol: str
    instrument: str = "spot"


class SettingsUpdateRequest(BaseModel):
    llm_provider: str | None = None
    llm_model: str | None = None
    llm_model_fast: str | None = None
    llm_base_url: str | None = None
    anthropic_api_key: str | None = None
    deepseek_api_key: str | None = None


class BacktestRequest(BaseModel):
    symbol: str | None = None
    bars: int = Field(default=300, ge=80, le=5000)
    window: int = Field(default=60, ge=20, le=1000)
    seed: int = Field(default=7, ge=0, le=1_000_000)
    drift: float = Field(default=0.001, ge=-0.05, le=0.05)
    vol: float = Field(default=0.01, gt=0, le=0.2)
    data: str = Field(default="synthetic", pattern="^(synthetic|cex)$")
    timeframe: str = "1h"


async def _latest_mark_price(symbol: str, venue, data_source) -> Decimal | None:
    try:
        snap = await data_source.snapshot(symbol)
        if snap.ohlcv:
            mark = snap.ohlcv[-1].close
            if hasattr(venue, "set_mark_price"):
                venue.set_mark_price(symbol, mark)
            return mark
    except Exception:  # noqa: BLE001
        pass

    try:
        return await venue.fetch_ticker(symbol)
    except Exception:  # noqa: BLE001
        return None


def _estimate_liq_price(position) -> Decimal | None:
    """无交易所返回的爆仓价时，用杠杆倒数近似（与 preflight 同款估算）。"""
    if position.instrument != "perp" or position.leverage <= 0:
        return None
    inv = Decimal(1) / Decimal(str(position.leverage))
    if position.side == "long":
        return position.entry_price * (Decimal(1) - inv)
    return position.entry_price * (Decimal(1) + inv)


async def _position_payload(position, venue, data_source) -> dict:
    mark: Decimal | None = None
    funding: Decimal | None = None
    try:
        snap = await data_source.snapshot(position.symbol)
        if snap.ohlcv:
            mark = snap.ohlcv[-1].close
            if hasattr(venue, "set_mark_price"):
                venue.set_mark_price(position.symbol, mark)
        if snap.derivatives is not None:
            funding = snap.derivatives.funding_rate
    except Exception:  # noqa: BLE001
        pass
    if mark is None:
        try:
            mark = await venue.fetch_ticker(position.symbol)
        except Exception:  # noqa: BLE001
            mark = None
    mark = mark or position.entry_price
    direction = Decimal(1) if position.side == "long" else Decimal(-1)
    entry_notional = position.size_base * position.entry_price
    unrealized = position.size_base * (mark - position.entry_price) * direction
    unrealized_pct = unrealized / entry_notional if entry_notional > 0 else Decimal(0)
    liq = position.liq_price or _estimate_liq_price(position)
    margin = position.margin_used()
    payload = position.model_dump(mode="json")
    payload.update({
        "mark_price": str(mark),
        "notional": str(position.size_base * mark),
        "unrealized_pnl": str(unrealized),
        "unrealized_pnl_pct": str(unrealized_pct),
        "liq_price": str(liq) if liq is not None else None,
        "margin_used": str(margin) if margin is not None else None,
        "funding_rate": str(funding) if funding is not None and position.instrument == "perp" else None,
    })
    return payload


async def _close_app_resources(app: FastAPI) -> None:
    for task in list(app.state.tasks):
        task.cancel()
    if app.state.tasks:
        await asyncio.gather(*app.state.tasks, return_exceptions=True)

    seen: set[int] = set()
    venues = [app.state.venue, *app.state.registry.all()]
    for v in venues:
        marker = id(v)
        if marker in seen:
            continue
        seen.add(marker)
        close = getattr(v, "close", None)
        if close is not None:
            await close()


def create_app(settings: Settings | None = None, data_source=None, venue=None) -> FastAPI:
    settings = settings or get_settings()
    events = EventBus()
    gate = PendingApprovalGate(timeout=settings.risk.approval_timeout_seconds, events=events)
    registry = build_registry(settings)
    venue = venue or registry.get(settings.execution_venue)
    if data_source is not None:
        data = data_source
    elif settings.data_source == "cex":
        data_venue = venue if getattr(venue, "kind", None) == "cex" else registry.get(settings.cex_id)
        data = CexMarketData(data_venue)
    else:
        data = SyntheticMarketData(live_ticks=settings.execution_venue == "paper")
    others = [v for v in registry.all() if getattr(v, "id", None) != getattr(venue, "id", None)]
    aggregator = MarketAggregator([v for v in registry.all() if getattr(v, "kind", None) == "cex"])
    # CYP_APPROVAL=auto → 策略化自动审批（白名单+低风险+小额），不满足仍转仪表盘人工门
    orch = Orchestrator(settings=settings, data_source=data, venue=venue, events=events,
                        approval=wrap_with_policy(settings, gate), risk_venues=[venue, *others])

    @contextlib.asynccontextmanager
    async def lifespan(app: FastAPI):
        engine = None
        if settings.runtime_autostart:   # CYP_RUNTIME_AUTOSTART=1：常驻扫描+监控双循环
            from cyp.runtime import build_engine
            engine = build_engine(settings, orch, venue, events=events)
            await engine.start()
        app.state.runtime_engine = engine
        try:
            yield
        finally:
            if engine is not None:
                await engine.stop()
            await _close_app_resources(app)

    app = FastAPI(title="cyp-agent", version="0.1.0", lifespan=lifespan)
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
            with contextlib.suppress(asyncio.QueueFull):
                q.put_nowait(evt)
    events.subscribe(_broadcast)

    @app.get("/api/health")
    async def health():
        display_mode = settings.mode
        if settings.execution_venue == "okx" and settings.okx_demo:
            display_mode = "OKX Demo"
        elif settings.execution_venue != "paper":
            display_mode = settings.execution_venue.upper()
        return {
            "ok": True,
            "mode": settings.mode,
            "display_mode": display_mode,
            "execution_venue": settings.execution_venue,
            "llm": settings.llm_enabled,
            "kill": settings.kill,
        }

    @app.get("/api/venues")
    async def venues():
        return registry.describe()

    @app.get("/api/settings")
    async def runtime_settings():
        from cyp.live import LiveGuard
        rc = settings.risk
        bc = settings.budget
        guard = LiveGuard.check(settings)
        return {
            "mode": settings.mode,
            "approval": settings.approval,
            "kill": settings.kill,
            "allow_perp": settings.allow_perp,
            "execution_venue": settings.execution_venue,
            "data_source": settings.data_source,
            "llm_enabled": settings.llm_enabled,
            "llm_provider": settings.llm_provider,
            "llm_model": settings.llm_model,
            "llm_model_fast": settings.llm_model_fast,
            "llm_base_url": settings.llm_base_url,
            "cex_id": settings.cex_id,
            "cex_trading_configured": settings.cex_trading_enabled,
            "okx": {"configured": settings.okx_configured, "demo": settings.okx_demo},
            "watchlist": settings.watchlist_symbols(),
            "intervals": {"scan": settings.scan_interval, "monitor": settings.monitor_interval},
            "runtime": {"max_concurrency": settings.max_concurrency, "log_level": settings.log_level,
                        "autostart": settings.runtime_autostart},
            "risk": {
                "max_risk_per_trade": str(rc.max_risk_per_trade),
                "max_position_pct": str(rc.max_position_pct),
                "max_gross_exposure": str(rc.max_gross_exposure),
                "max_symbol_concentration": str(rc.max_symbol_concentration),
                "max_correlated_exposure": str(rc.max_correlated_exposure),
                "max_cvar_pct": str(rc.max_cvar_pct),
                "max_orders_per_hour": rc.max_orders_per_hour,
                "max_slippage_bps": str(rc.max_slippage_bps),
                "max_leverage": str(rc.max_leverage),
                "min_liq_buffer": str(rc.min_liq_buffer),
                "force_isolated": rc.force_isolated,
                "min_margin_ratio": str(rc.min_margin_ratio),
                "daily_drawdown_limit": str(rc.daily_drawdown_limit),
                "weekly_drawdown_limit": str(rc.weekly_drawdown_limit),
                "max_drawdown_limit": str(rc.max_drawdown_limit),
                "max_consecutive_losses": rc.max_consecutive_losses,
                "approval_timeout_seconds": rc.approval_timeout_seconds,
            },
            "budget": {
                "max_iterations": bc.max_iterations,
                "max_tokens": bc.max_tokens,
                "max_cost_usd": bc.max_cost_usd,
                "max_wall_seconds": bc.max_wall_seconds,
            },
            "live_guard": {"ok": guard.ok, "reasons": guard.reasons},
        }

    @app.post("/api/settings")
    async def update_runtime_settings(req: SettingsUpdateRequest):
        if req.llm_provider is not None:
            if req.llm_provider not in {"anthropic", "deepseek"}:
                raise HTTPException(status_code=422, detail="llm_provider must be anthropic or deepseek")
            settings.llm_provider = req.llm_provider

        if req.llm_model is not None:
            settings.llm_model = req.llm_model.strip()
        if req.llm_model_fast is not None:
            settings.llm_model_fast = req.llm_model_fast.strip()
        if req.llm_base_url is not None:
            settings.llm_base_url = req.llm_base_url.strip() or None
        if req.anthropic_api_key and req.anthropic_api_key.strip():
            settings.anthropic_api_key = req.anthropic_api_key.strip()
        if req.deepseek_api_key and req.deepseek_api_key.strip():
            settings.deepseek_api_key = req.deepseek_api_key.strip()

        app.state.orch.llm = build_llm(settings)
        return await runtime_settings()

    @app.get("/api/market")
    async def market(symbol: str | None = None):
        sym = symbol or settings.watchlist_symbols()[0]
        summary = await aggregator.summary(sym)
        buy_v, buy_p = summary.best_buy
        sell_v, sell_p = summary.best_sell
        return {"symbol": sym, "tickers": {k: str(v) for k, v in summary.tickers.items()},
                "best_buy": {"venue": buy_v, "price": str(buy_p) if buy_p else None},
                "best_sell": {"venue": sell_v, "price": str(sell_p) if sell_p else None},
                "spread_bps": str(summary.spread_bps) if summary.spread_bps is not None else None,
                "funding_rates": {k: str(v) for k, v in summary.funding_rates.items()},
                "arb_hints": summary.arb_hints}

    @app.get("/api/positions")
    async def positions():
        return [await _position_payload(p, venue, data) for p in await venue.positions()]

    @app.post("/api/positions/close")
    async def close_position(req: ClosePositionRequest):
        positions = await venue.positions()
        pos = next((p for p in positions if p.symbol == req.symbol and p.instrument == req.instrument), None)
        if pos is None:
            raise HTTPException(status_code=404, detail="无此持仓")

        mark = await _latest_mark_price(pos.symbol, venue, data) or pos.entry_price
        refreshed = await venue.positions()
        if not any(p.symbol == req.symbol and p.instrument == req.instrument for p in refreshed):
            return {
                "client_id": "protective-close",
                "status": "filled",
                "filled_base": str(pos.size_base),
                "avg_price": str(mark),
                "fee_quote": "0",
                "slippage_bps": "0",
                "protective_orders": [],
            }
        intent = OrderIntent(
            client_id=f"manual-close-{uuid.uuid4().hex[:12]}",
            symbol=pos.symbol,
            venue=getattr(venue, "id", pos.venue),
            side=pos.side,
            instrument=pos.instrument,
            order_type="market",
            size_quote=pos.size_base * mark,
            leverage=pos.leverage,
            reduce_only=True,
        )
        result = await venue.place(intent)
        if result.status != "filled":
            raise HTTPException(status_code=400, detail=result.error or "平仓失败")
        return result.model_dump(mode="json")

    @app.get("/api/metrics")
    async def metrics():
        return {"runs": orch.metrics.snapshot(), "llm": orch.llm.metrics.snapshot()}

    @app.get("/api/risk")
    async def risk():
        from cyp.live import LiveGuard
        from cyp.portfolio import aggregate_positions
        bal = await venue.balances()
        equity = bal.total_quote if bal.total_quote > 0 else bal.free_quote
        snap = orch.portfolio.risk_snapshot(equity)
        rc = settings.risk
        guard = LiveGuard.check(settings)
        # 维持保证金率 = 净值 / 永续总名义（入场价近似；无永续仓为 None）
        positions = await aggregate_positions(orch.risk_venues)
        perp_notional = sum((p.notional_at(p.entry_price) for p in positions
                             if p.instrument == "perp"), Decimal(0))
        margin_ratio = (equity / perp_notional) if perp_notional > 0 else None
        return {
            "mode": settings.mode, "kill": settings.kill, "equity": str(equity),
            "drawdown": {k: str(snap[f"{k}_drawdown"]) for k in ("daily", "weekly", "total")},
            "orders_last_hour": snap["orders_last_hour"],
            "consecutive_losses": snap["consecutive_losses"],
            "margin_ratio": str(margin_ratio) if margin_ratio is not None else None,
            "perp_notional": str(perp_notional),
            "limits": {"daily_dd": str(rc.daily_drawdown_limit), "weekly_dd": str(rc.weekly_drawdown_limit),
                       "total_dd": str(rc.max_drawdown_limit), "max_leverage": str(rc.max_leverage),
                       "max_orders_per_hour": rc.max_orders_per_hour,
                       "max_consecutive_losses": rc.max_consecutive_losses,
                       "min_margin_ratio": str(rc.min_margin_ratio)},
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
        by_symbol = [{"symbol": e["symbol"], "cluster": e["cluster"],
                      "long": str(e["long"]), "short": str(e["short"])}
                     for e in view.symbol_breakdown()]
        return {"equity": str(equity), "n_positions": len(positions),
                "gross": str(view.gross_notional()), "clusters": clusters,
                "by_symbol": by_symbol,
                "correlated_limit": str(equity * settings.risk.max_correlated_exposure)}

    @app.post("/api/backtest")
    async def backtest(req: BacktestRequest):
        if req.window >= req.bars:
            raise HTTPException(status_code=422, detail="window must be smaller than bars")

        symbol = req.symbol or settings.watchlist_symbols()[0]
        if req.data == "cex":
            from cyp.backtest import OhlcvArchive
            hist_venue = venue if getattr(venue, "kind", None) == "cex" else registry.get(settings.cex_id)
            try:
                candles = await OhlcvArchive(settings.db_url).ensure(hist_venue, symbol,
                                                                     req.timeframe, req.bars)
            except Exception as e:  # noqa: BLE001
                raise HTTPException(status_code=502, detail=f"真实历史拉取失败：{e}") from e
            if len(candles) <= req.window:
                raise HTTPException(status_code=502,
                                    detail=f"真实历史不足：{len(candles)} 根（需要 > window={req.window}）")
        else:
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
            "data": req.data,
            "timeframe": req.timeframe,
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
        ok = gate.resolve(run_id, req.decision, size=req.size, note=req.note, operator=req.operator)
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
