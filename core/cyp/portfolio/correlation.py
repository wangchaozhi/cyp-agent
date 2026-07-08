"""相关性聚类：加密资产系统性相关，同一簇内的同向敞口应合并限额。

M4 用粗粒度静态聚类（majors / alt），可用；后续可升级为滚动收益率相关性矩阵。
"""

from __future__ import annotations

_MAJORS = {"BTC", "ETH", "BNB", "SOL", "XRP", "ADA", "DOGE", "TON", "AVAX"}


def base_asset(symbol: str) -> str:
    """'BTC/USDT' / 'BTC/USDT:USDT' → 'BTC'。"""
    return symbol.split("/")[0].split(":")[0].upper()


class CorrelationModel:
    def __init__(self, majors: set[str] | None = None) -> None:
        self.majors = majors or _MAJORS

    def cluster_of(self, symbol: str) -> str:
        return "major" if base_asset(symbol) in self.majors else "alt"
