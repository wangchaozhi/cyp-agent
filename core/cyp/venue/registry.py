"""Venue 注册表：新增场所在此 register 一行，仪表盘 GET /api/venues 自动展示。"""

from __future__ import annotations

from cyp.config import Settings
from cyp.venue.base import Venue, VenueCaps
from cyp.venue.cex import CexVenue
from cyp.venue.paper import PaperVenue


class VenueRegistry:
    def __init__(self) -> None:
        self._venues: dict[str, Venue] = {}

    def register(self, venue: Venue) -> None:
        self._venues[venue.id] = venue

    def get(self, venue_id: str) -> Venue:
        return self._venues[venue_id]

    def all(self) -> list[Venue]:
        return list(self._venues.values())

    def describe(self) -> list[dict]:
        """供 GET /api/venues：场所能力与配置状态。"""
        out = []
        for v in self._venues.values():
            caps: VenueCaps = v.caps
            out.append({
                "id": v.id, "kind": v.kind, "configured": v.is_configured(),
                "spot": caps.spot, "perp": caps.perp,
                "native_protective_orders": caps.native_protective_orders,
                "read_only": caps.read_only,
            })
        return out


def build_registry(settings: Settings) -> VenueRegistry:
    """按配置组装默认注册表：PaperVenue + CexVenue（参考实现 Binance）。

    CexVenue 仅在 mode=live 且通过 LiveGuard 时才可下单，否则保持只读（安全默认）。
    """
    from cyp.live import LiveGuard
    guard = LiveGuard.check(settings)
    cex_read_only = not (settings.mode == "live" and guard.ok)

    reg = VenueRegistry()
    reg.register(PaperVenue())
    reg.register(CexVenue(
        exchange_id=settings.cex_id,
        api_key=settings.binance_api_key,
        api_secret=settings.binance_api_secret,
        read_only=cex_read_only,
    ))
    return reg
