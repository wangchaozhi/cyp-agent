"""数据管线：采集行情/衍生品/情绪并组装 MarketSnapshot；技术指标计算。"""

from cyp.data.indicators import indicator_snapshot
from cyp.data.market import (
    CexMarketData,
    MarketDataSource,
    SyntheticMarketData,
    build_data_source,
)
from cyp.data.onchain import OnchainDataSource
from cyp.data.volatility import (
    ewma_vol_from_candles,
    ewma_volatility,
    realized_volatility,
    simple_returns,
)

__all__ = [
    "indicator_snapshot",
    "MarketDataSource",
    "CexMarketData",
    "SyntheticMarketData",
    "build_data_source",
    "OnchainDataSource",
    "ewma_volatility",
    "ewma_vol_from_candles",
    "realized_volatility",
    "simple_returns",
]
