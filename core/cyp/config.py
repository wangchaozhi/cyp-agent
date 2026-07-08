"""配置：从环境变量/.env 加载，字段与 .env.example 一一对应。

- RiskConfig / BudgetConfig 是独立可测的 BaseSettings（测试可直接构造覆盖阈值）。
- 秘密（交易所 Key / LLM Key）只从 env 读，绝不写死、绝不落盘。
- 默认值 = paper 模式 + 无密钥，可端到端跑通。
"""

from __future__ import annotations

from decimal import Decimal
from functools import lru_cache
from typing import Literal

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

_ENV = SettingsConfigDict(env_prefix="CYP_", env_file=".env", env_file_encoding="utf-8",
                          extra="ignore", populate_by_name=True)


class RiskConfig(BaseSettings):
    """确定性风控引擎的阈值（详见 docs/RISK.md）。实盘应比默认更保守。"""

    model_config = _ENV

    # 通用
    max_risk_per_trade: Decimal = Decimal("0.01")      # 单笔风险 ≤ 账户净值 1%
    max_position_pct: Decimal = Decimal("0.20")        # 单仓名义上限
    max_gross_exposure: Decimal = Decimal("1.00")      # 总敞口上限
    max_symbol_concentration: Decimal = Decimal("0.30")
    max_orders_per_hour: int = 10
    max_slippage_bps: Decimal = Decimal("30")
    # 合约
    max_leverage: Decimal = Decimal("3")
    min_liq_buffer: Decimal = Decimal("0.30")          # 入场到爆仓价最小缓冲
    force_isolated: bool = True                         # 强制逐仓（风险隔离）
    min_margin_ratio: Decimal = Decimal("0.05")        # 账户维持保证金率下限
    # 链上
    max_price_impact: Decimal = Decimal("0.01")
    max_gas_gwei: Decimal | None = None
    # 熔断
    daily_drawdown_limit: Decimal = Decimal("0.03")
    weekly_drawdown_limit: Decimal = Decimal("0.08")
    max_drawdown_limit: Decimal = Decimal("0.15")
    max_consecutive_losses: int = 4
    # 审批
    approval_timeout_seconds: int = 1800               # 开仓审批超时=拒绝


class BudgetConfig(BaseSettings):
    """LLM 成本四重硬上限，任一触发即优雅终止。"""

    model_config = _ENV

    max_iterations: int = 20
    max_tokens: int = 200_000
    max_cost_usd: float = 2.0
    max_wall_seconds: int = 300


class Settings(BaseSettings):
    model_config = _ENV

    # 运行模式
    mode: Literal["paper", "live"] = "paper"
    approval: Literal["cli", "dashboard", "auto"] = "cli"
    kill: bool = False
    allow_perp: bool = False        # 允许策略官提出永续合约（默认仅现货，更保守）

    # LLM（缺失则降级为规则模板）
    llm_model: str = "claude-opus-4-8"
    llm_model_fast: str = "claude-haiku-4-5-20251001"
    anthropic_api_key: str | None = Field(default=None, validation_alias="ANTHROPIC_API_KEY")

    # CEX（参考实现 = Binance；只读行情无需 Key）
    cex_id: str = "binance"
    binance_api_key: str | None = Field(default=None, validation_alias="BINANCE_API_KEY")
    binance_api_secret: str | None = Field(default=None, validation_alias="BINANCE_API_SECRET")
    live_ack: bool = False          # 实盘确认（防误开实盘）；mode=live 时必须为 True

    # OKX（模拟交易 = OKX Demo Trading；需 API passphrase）
    okx_api_key: str | None = Field(default=None, validation_alias="OKX_API_KEY")
    okx_api_secret: str | None = Field(default=None, validation_alias="OKX_API_SECRET")
    okx_password: str | None = Field(default=None, validation_alias="OKX_PASSWORD")
    okx_demo: bool = True           # True = OKX Demo（模拟盘，sandbox）；实盘需显式关

    # 告警
    alert_webhook: str | None = None

    @property
    def okx_configured(self) -> bool:
        return bool(self.okx_api_key and self.okx_api_secret and self.okx_password)

    # 链上（M3）
    evm_rpc_url: str | None = None
    signer: Literal["keystore", "kms", "hardware"] = "keystore"

    # 运行时（三条循环）
    scan_interval: int = 300
    monitor_interval: int = 15
    watchlist: str = "BTC/USDT"
    max_concurrency: int = 2

    # 持久化 & 日志
    db_path: str = "./data/cyp.db"
    log_level: str = "INFO"

    # 嵌套子配置（各自读同一批 CYP_ 环境变量）
    risk: RiskConfig = Field(default_factory=RiskConfig)
    budget: BudgetConfig = Field(default_factory=BudgetConfig)

    @property
    def llm_enabled(self) -> bool:
        return bool(self.anthropic_api_key)

    @property
    def cex_trading_enabled(self) -> bool:
        return bool(self.binance_api_key and self.binance_api_secret)

    def watchlist_symbols(self) -> list[str]:
        return [s.strip() for s in self.watchlist.split(",") if s.strip()]


@lru_cache
def get_settings() -> Settings:
    """进程级单例；测试可直接构造 Settings(...) 绕过缓存。"""

    return Settings()
