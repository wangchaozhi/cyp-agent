"""实盘前置校验 + 告警派发 + 熔断告警接入编排器。离线。"""

import asyncio
from datetime import datetime, timezone
from decimal import Decimal

from cyp.alerts import Alerter
from cyp.config import Settings
from cyp.contracts import Candle, DerivativesData, MarketSnapshot, SentimentData
from cyp.live import LiveGuard
from cyp.orchestrator import Orchestrator
from cyp.venue import PaperVenue, build_registry

run = asyncio.run


# ---- LiveGuard -------------------------------------------------------------

def test_paper_mode_guard_ok():
    assert LiveGuard.check(Settings(_env_file=None)).ok


def test_live_without_keys_or_ack_blocked():
    r = LiveGuard.check(Settings(_env_file=None, mode="live"))
    assert not r.ok and any("API Key" in x for x in r.reasons)


def test_live_all_conditions_met_ok():
    s = Settings(_env_file=None, mode="live", live_ack=True,
                 binance_api_key="k", binance_api_secret="s")
    assert LiveGuard.check(s).ok


def test_registry_keeps_cex_readonly_when_guard_fails():
    # live 但缺 key/ack → CexVenue 必须保持只读（安全默认）
    reg = build_registry(Settings(_env_file=None, mode="live"))
    cex = next(d for d in reg.describe() if d["id"] == "binance")
    assert cex["read_only"] is True


def test_registry_enables_cex_trading_when_guard_passes():
    s = Settings(_env_file=None, mode="live", live_ack=True,
                 binance_api_key="k", binance_api_secret="s")
    reg = build_registry(s)
    cex = next(d for d in reg.describe() if d["id"] == "binance")
    assert cex["read_only"] is False


# ---- Alerter ---------------------------------------------------------------

class FakeSink:
    def __init__(self): self.alerts = []
    async def emit(self, alert): self.alerts.append(alert)


def test_alerter_dispatches_and_redacts():
    sink = FakeSink()
    a = Alerter([sink])
    run(a.alert("error", "boom", api_key="sk-secret", symbol="BTC/USDT"))
    assert sink.alerts[0]["msg"] == "boom"
    assert sink.alerts[0]["api_key"] == "***"       # 脱敏
    assert sink.alerts[0]["symbol"] == "BTC/USDT"


def test_alerter_isolates_sink_failure():
    class Boom:
        async def emit(self, a): raise RuntimeError("x")
    ok = FakeSink()
    run(Alerter([Boom(), ok]).alert("warning", "m"))
    assert ok.alerts                                 # 一个 sink 失败不影响另一个


def test_kill_switch_rejection_raises_alert():
    class Uptrend:
        async def snapshot(self, symbol):
            candles = [Candle(ts=datetime.now(timezone.utc), open=Decimal(50000 + i * 125),
                              high=Decimal(50000 + i * 125 + 50), low=Decimal(50000 + i * 125 - 50),
                              close=Decimal(50000 + i * 125), volume=Decimal("100")) for i in range(80)]
            return MarketSnapshot(symbol=symbol, venue="x", ohlcv=candles,
                                  derivatives=DerivativesData(funding_rate=Decimal("-0.0005"),
                                                              long_short_ratio=Decimal("0.8")),
                                  sentiment=SentimentData(fear_greed=20))
    sink = FakeSink()
    orch = Orchestrator(Settings(_env_file=None, kill=True), Uptrend(), PaperVenue(),
                        alerter=Alerter([sink]))
    res = run(orch.run_once("BTC/USDT"))
    assert res.status == "rejected"
    assert any(a["msg"] == "risk_circuit" for a in sink.alerts)   # 熔断/Kill 告警
