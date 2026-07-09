"""链上数据管线 stub（M3）：聪明钱/池深/持有分布/交易所净流。

无 RPC/Key 时返回 None（分析师降级），有注入 fetcher 时填充 OnchainData。
真实数据源（The Graph / Nansen / Dune 等）在实操阶段接入，本模块保证接口稳定。
"""

from __future__ import annotations

from cyp.contracts import OnchainData


class OnchainDataSource:
    """可注入 fetcher 的链上数据源：fetcher(symbol) -> dict | None。

    与 MarketSnapshot 组装方并列使用：
        onchain = await OnchainDataSource(fetcher).fetch(symbol)
        snap = MarketSnapshot(..., onchain=onchain)
    """

    def __init__(self, fetcher=None) -> None:
        self._fetcher = fetcher

    def is_configured(self) -> bool:
        return self._fetcher is not None

    async def fetch(self, symbol: str) -> OnchainData | None:
        if self._fetcher is None:
            return None   # 无数据源 → 分析师按 degraded 处理
        try:
            raw = await self._fetcher(symbol)
        except Exception:  # noqa: BLE001 —— 数据源失败隔离，降级不炸管线
            return None
        if not raw:
            return None
        return OnchainData(**raw)
