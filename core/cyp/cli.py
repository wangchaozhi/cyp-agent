"""cyp 命令行入口：跑一轮端到端闭环（M0）。

    python -m cyp.cli --symbol BTC/USDT --data synthetic --approve auto
    cyp --symbol BTC/USDT --approve cli          # 人工审批

默认 synthetic 数据 + auto 审批时，零密钥零网络即可端到端演示 7 步。
"""

from __future__ import annotations

import argparse
import asyncio
import sys

from cyp.approval import AutoApprove
from cyp.approval.cli import CLIApprovalGate
from cyp.config import Settings
from cyp.data import CexMarketData, SyntheticMarketData
from cyp.events import EventBus
from cyp.orchestrator import Orchestrator, RunResult
from cyp.venue import PaperVenue, build_registry

_STEP_LABEL = {
    "run_started": "▶ 开始", "snapshot_ready": "① 采集", "reports_ready": "② 分析",
    "proposal_ready": "③ 决策", "risk_assessed": "④ 风控", "approval_decided": "⑤ 审批",
    "executed": "⑥ 执行", "reviewed": "⑦ 复盘", "run_failed": "✖ 失败",
}


def _on_event(evt: dict) -> None:
    label = _STEP_LABEL.get(evt["type"], evt["type"])
    extra = ""
    if evt["type"] == "reports_ready":
        extra = "  " + " | ".join(f"{r['agent']}:{r['stance']}({r['confidence']:.2f})"
                                  f"{'*' if r['degraded'] else ''}" for r in evt["reports"])
    elif evt["type"] == "proposal_ready":
        p = evt["proposal"]
        extra = f"  {p['side']} 规模={p['size_quote']} 止损={p['stop_loss']} 置信={p['confidence']:.2f}"
    elif evt["type"] == "risk_assessed":
        a = evt["assessment"]
        extra = f"  {a['verdict']} risk={a['risk_score']:.2f}"
    elif evt["type"] == "executed":
        e = evt["execution"]
        extra = f"  {e['status']} 均价={e.get('avg_price')} 保护单={len(e['protective_orders'])}"
    print(f"  {label}{extra}")


def _summary(res: RunResult) -> None:
    print("\n" + "-" * 56)
    print(f"结果：{res.status}   run_id={res.run_id}   symbol={res.symbol}")
    if res.status == "executed":
        e = res.execution
        print(f"  已在模拟盘成交：{res.proposal.side} 均价={e.avg_price} 滑点={e.slippage_bps}bps")
        print(f"  保护单：{[(p.kind, str(p.trigger_price)) for p in e.protective_orders]}")
        if res.review and res.review.lessons:
            print(f"  复盘经验：{res.review.lessons}")
    elif res.status == "rejected":
        print(f"  风控否决：{res.assessment.hard_violations}")
    elif res.status == "not_approved":
        print("  人工拒绝，未下单。")
    elif res.status == "no_trade":
        print("  信号不足，本轮不开仓。")
    print("-" * 56)


def _build(args, settings: Settings) -> Orchestrator:
    venue = PaperVenue()
    if args.data == "cex":
        registry = build_registry(settings)
        data = CexMarketData(registry.get(settings.cex_id))
    else:
        data = SyntheticMarketData()
    approval = AutoApprove() if args.approve == "auto" else CLIApprovalGate(
        timeout=settings.risk.approval_timeout_seconds)
    events = EventBus()
    events.subscribe(_on_event)
    return Orchestrator(settings=settings, data_source=data, venue=venue,
                        events=events, approval=approval)


def main(argv: list[str] | None = None) -> int:
    # Windows 控制台常为 GBK，重配为 UTF-8 以正确输出步骤符号与中文
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
        except Exception:
            pass
    settings = Settings()
    from cyp.observability import configure_logging
    configure_logging(settings.log_level)
    parser = argparse.ArgumentParser(prog="cyp", description="cyp-agent 端到端闭环（M0）")
    parser.add_argument("--symbol", default=settings.watchlist_symbols()[0])
    parser.add_argument("--data", choices=["synthetic", "cex"], default="synthetic",
                        help="synthetic=离线合成（默认）；cex=只读真实行情")
    parser.add_argument("--approve", choices=["auto", "cli"], default="cli",
                        help="auto=自动批准（演示）；cli=人工审批（半自动）")
    parser.add_argument("--loop", type=int, default=0,
                        help="0=跑一轮；N>0=运行时引擎跑 N 轮扫描（含启动对账+持仓监控）")
    args = parser.parse_args(argv)

    print(f"cyp-agent · mode={settings.mode} · llm={'on' if settings.llm_enabled else 'off(规则降级)'} "
          f"· data={args.data} · approve={args.approve}")
    orch = _build(args, settings)
    if args.loop > 0:
        from cyp.runtime import build_engine
        engine = build_engine(settings, orch, orch.venue, events=orch.events)
        asyncio.run(engine.run_bounded(scan_cycles=args.loop, monitor_cycles=1))
        print(f"\n运行时跑完 {args.loop} 轮扫描。指标：{orch.metrics.snapshot()}")
    else:
        res = asyncio.run(orch.run_once(args.symbol))
        _summary(res)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
