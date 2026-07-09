"""可观测性：脱敏 / trace-span / 运行指标 / 编排器接入。离线。"""

import asyncio
from decimal import Decimal

from cyp.observability import RunMetrics, Trace, redact


def test_redact_masks_secrets_recursively():
    obj = {"api_key": "sk-123", "nested": {"private_key": "0xabc", "ok": 1},
           "list": [{"authorization": "Bearer x"}], "symbol": "BTC/USDT"}
    out = redact(obj)
    assert out["api_key"] == "***"
    assert out["nested"]["private_key"] == "***" and out["nested"]["ok"] == 1
    assert out["list"][0]["authorization"] == "***"
    assert out["symbol"] == "BTC/USDT"       # 非敏感原样保留


def test_trace_records_spans():
    async def scenario():
        tr = Trace("run1")
        async with tr.span("collect"):
            await asyncio.sleep(0)
        async with tr.span("analyze"):
            await asyncio.sleep(0)
        return tr
    tr = asyncio.run(scenario())
    s = tr.summary()
    assert s["trace_id"] == "run1"
    assert [sp["name"] for sp in s["spans"]] == ["collect", "analyze"]
    assert all(sp["status"] == "ok" for sp in s["spans"])


def test_trace_marks_error_span():
    async def scenario():
        tr = Trace("r")
        try:
            async with tr.span("boom"):
                raise ValueError("x")
        except ValueError:
            pass
        return tr
    tr = asyncio.run(scenario())
    assert tr.spans[0].status == "error" and tr.spans[0].error == "x"


def test_run_metrics_counts_and_rates():
    m = RunMetrics()
    m.record("executed", Decimal("10"))
    m.record("executed", Decimal("20"))
    m.record("not_approved")
    m.record("error")
    snap = m.snapshot()
    assert snap["executed"] == 2 and snap["not_approved"] == 1 and snap["errors"] == 1
    assert snap["avg_slippage_bps"] == 15.0
    assert snap["approval_rate"] == round(2 / 3, 3)   # executed/(executed+not_approved)


def test_run_metrics_slo_fields():
    m = RunMetrics()
    m.record("executed", Decimal("3"))      # 桶 0-5
    m.record("executed", Decimal("12"))     # 桶 5-15
    m.record("executed", Decimal("40"))     # 桶 30+
    m.record("execution_failed")
    m.record_approval_latency(2.0)
    m.record_approval_latency(4.0)
    snap = m.snapshot()
    assert snap["slippage_hist_bps"] == {"0-5": 1, "5-15": 1, "15-30": 0, "30+": 1}
    assert snap["order_success_rate"] == 0.75           # 3 成 / 1 败
    assert snap["approval_latency"]["avg_s"] == 3.0
    assert snap["approval_latency"]["max_s"] == 4.0
    assert snap["approval_latency"]["n"] == 2


def test_orchestrator_emits_trace_in_run_done():
    from cyp.config import Settings
    from cyp.data import SyntheticMarketData
    from cyp.events import EventBus
    from cyp.orchestrator import Orchestrator
    from cyp.venue import PaperVenue

    seen = {}
    events = EventBus()
    events.subscribe(lambda e: seen.update({e["type"]: e}) if e["type"] == "run_done" else None)
    orch = Orchestrator(Settings(_env_file=None), SyntheticMarketData(), PaperVenue(), events=events)
    asyncio.run(orch.run_once("BTC/USDT"))
    assert "run_done" in seen
    assert "trace" in seen["run_done"] and seen["run_done"]["trace"]["spans"]
    assert orch.metrics.snapshot()["runs"] == 1
