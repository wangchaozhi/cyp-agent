"""M4 组合级风控：跨场所聚合 + 相关性簇同向敞口护栏。离线。"""

import asyncio
from decimal import Decimal

from cyp.config import RiskConfig
from cyp.contracts import Position, TradeProposal
from cyp.portfolio import CorrelationModel, PortfolioView, aggregate_positions, base_asset
from cyp.risk import assess
from cyp.risk.rules import RiskContext
from cyp.venue import PaperVenue

run = asyncio.run
CFG = RiskConfig(_env_file=None)


def _pos(symbol, side="long", size_base="0.1", entry="60000"):
    return Position(symbol=symbol, venue="paper", side=side, size_base=Decimal(size_base),
                    entry_price=Decimal(entry))


# ---- 相关性聚类 ------------------------------------------------------------

def test_base_asset_and_cluster():
    assert base_asset("BTC/USDT") == "BTC"
    assert base_asset("ETH/USDT:USDT") == "ETH"
    corr = CorrelationModel()
    assert corr.cluster_of("BTC/USDT") == "major"
    assert corr.cluster_of("ETH/USDT") == "major"
    assert corr.cluster_of("PEPE/USDT") == "alt"


# ---- 组合视图 --------------------------------------------------------------

def test_cluster_net_directional_sums_same_direction():
    view = PortfolioView([_pos("BTC/USDT", "long"), _pos("ETH/USDT", "long"),
                          _pos("PEPE/USDT", "long")])
    # majors 同向净敞口 = BTC(0.1*60000) + ETH(0.1*60000) = 12000（PEPE 属 alt 不计）
    net = view.cluster_net_directional("major", "long")
    assert net == Decimal("12000")


def test_cluster_net_directional_offsets_opposite():
    view = PortfolioView([_pos("BTC/USDT", "long"), _pos("ETH/USDT", "short")])
    # 一多一空 → 同向净敞口相抵为 0
    assert view.cluster_net_directional("major", "long") == Decimal("0")


def test_aggregate_positions_across_venues():
    v1, v2 = PaperVenue(), PaperVenue()
    v1._positions[("BTC/USDT", "spot")] = _pos("BTC/USDT")
    v2._positions[("ETH/USDT", "spot")] = _pos("ETH/USDT")
    agg = run(aggregate_positions([v1, v2]))
    assert {p.symbol for p in agg} == {"BTC/USDT", "ETH/USDT"}


# ---- 相关性敞口护栏 --------------------------------------------------------

def _prop(**over):
    base = {"symbol": "ETH/USDT", "venue": "paper", "side": "long", "size_quote": Decimal("1000"),
                "stop_loss": Decimal("58000"), "confidence": 0.7}
    base.update(over)
    return TradeProposal(**base)


def _ctx(**over):
    base = {"equity_quote": Decimal("10000"), "ref_price": Decimal("60000")}
    base.update(over)
    return RiskContext(**base)


def test_correlated_exposure_downsize():
    # 簇上限 = 10000 * 0.5 = 5000；已有同向 4500 → 只剩 500，缩仓
    r = assess(_prop(size_quote=Decimal("1000")),
               _ctx(correlated_exposure_quote=Decimal("4500")), CFG)
    assert r.verdict == "downsized"
    assert r.adjusted_size_quote == Decimal("500")


def test_correlated_exposure_full_rejected():
    r = assess(_prop(), _ctx(correlated_exposure_quote=Decimal("5000")), CFG)
    assert r.verdict == "rejected"
    assert any("correlated_exposure" in v for v in r.hard_violations)


def test_correlated_exposure_skipped_when_none():
    # 未计算相关性敞口（None）→ 规则跳过，不误伤
    r = assess(_prop(), _ctx(correlated_exposure_quote=None), CFG)
    assert r.verdict in ("approved", "downsized")
