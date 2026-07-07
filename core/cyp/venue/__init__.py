"""统一交易场所抽象：CEX / 链上 / 模拟盘对上层长得一样。"""

from cyp.venue.base import PreflightReport, Venue, VenueCaps, VenueKind
from cyp.venue.cex import CexVenue
from cyp.venue.paper import PaperVenue
from cyp.venue.registry import VenueRegistry, build_registry

__all__ = [
    "Venue",
    "VenueCaps",
    "VenueKind",
    "PreflightReport",
    "PaperVenue",
    "CexVenue",
    "VenueRegistry",
    "build_registry",
]
