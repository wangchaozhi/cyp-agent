# 更新日志

遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/)。版本遵循语义化版本。

## [未发布]

### ROADMAP 补齐 · M1/M3/M4/M5/M6 未完成项 + OKX Demo 联网实测

- **M1 仪表盘合约信息**：`Position` 增 `liq_price/margin_mode`（Paper 由 preflight 估算填充、
  Cex 取 ccxt `liquidationPrice/marginMode`）；`/api/positions` 补爆仓价/保证金占用/资金费率，
  `/api/risk` 补 `margin_ratio/perp_notional`；前端持仓表新增爆仓价/保证金/资金费列 +
  逐仓/全仓标注，风险面板新增保证金健康度。
- **M4 收尾**：策略官组合感知——同标的同向已有仓 → flat，相关簇同向敞口超 80% 上限 →
  缩量/flat（`Strategist.run` 接收聚合持仓）；`/api/portfolio` 增 `by_symbol`，前端敞口
  热力图（纯 CSS）；聚合器 `funding_rates()`/`arb_hints()`（跨所价差 bps/资金费差，仅提示），
  `/api/market` 扩展 + 新增 MarketPanel。
- **M5 收尾**：`OhlcvArchive`——ccxt 分页拉取真实 OHLCV 落 SQLite 增量缓存，
  `cyp.backtest.run/sweep --data cex` 与 `POST /api/backtest data=cex` 均可用；
  `Backtester` 挂接 `MemoryStore`、`BacktestReport.lessons` 汇总复盘经验；
  strategist/risk_officer LLM 提示词注入 `ctx.lessons`（规则路径不变）。
- **M6 进阶自动化**：`PolicyApprovalGate` 策略化自动审批（白名单 + risk_score + 金额上限，
  否则转人工），`settings.approval=auto` 在 CLI/FastAPI 接线；`MemoryStore` 迁 SQLite +
  按 symbol/词元相关性检索（旧 JSON 自动迁移）；`PositionMonitor` 增强（止损/爆仓逼近、
  EWMA 异常波动、保证金率告警走 Alerter），`CYP_RUNTIME=1` 时 FastAPI 启动 RuntimeEngine；
  审批 `operator` 透传入审计；`RunMetrics` 增审批时延/滑点分布/下单成功率 SLO，
  `/api/metrics` 暴露 + OverviewStrip 展示。
- **M3 链上骨架（mock client 离线测）**：`OnchainVenue`——「精确额度 approve → swap」两步
  执行、nonce 管理、确认跟踪、revert 处理、幂等去重、`reconcile_onchain` 对账；隔离签名器
  `onchain/signer.py`（keystore，私钥不落日志；KMS/硬件留接口）；风控 §2.3 五条护栏
  （禁无限授权/合约白名单/最小池 TVL/gas 上限/MEV 私有内存池）+ `est_price_impact` 接入
  RiskContext；`OnchainDataSource` stub；前端持仓表标注链上仓位。
- **OKX Demo 联网实测**：新增可重复 smoke 脚本 `python -m cyp.tools.okx_smoke`——
  配置校验→余额→现货小额下单（带止损/止盈保护单）→幂等重放→撤保护单→平仓清理→增量对账；
  已联网全绿，作为 M2 真实网络项的 OKX 版验收（Binance Testnet 不采用，以 OKX Demo 替代）。

### 文档 · 数学模型规格完善
- 新增 `docs/quant/` 规格分册：validation / stats / risk / sizing / portfolio /
  signals_execution，补齐公式、默认阈值、数据要求、降级路径和测试清单。
- 同步 Q1 量化状态：`validate.py`、`pbo.py`、`stats.py`、EWMA 波动率、
  波动率目标仓位、VaR/CVaR 基础件已落地；成本模型、协方差/组合优化仍待实现。

### Q1 · 尾部风险护栏
- 新增 `risk/measures.py`：非负损失序列、Historical VaR、CVaR / Expected Shortfall、
  `tail_risk_quote` 计价币尾部风险。
- 新增 `rule_cvar_limit`：`portfolio_cvar_quote > equity * max_cvar_pct` 时拒绝新开仓；
  默认 `CYP_MAX_CVAR_PCT=0.03`，平仓/减仓仍放行。

### M5（部分）· 策略参数化择优 + 回测引擎
- **策略参数化**：`StrategyConfig` 打包策略官可调参（分析师权重/入场阈值/ATR 止损止盈
  倍数/单笔风险），策略官配置驱动；`grid()` 笛卡尔积 + `sweep()` 批量回测按目标函数
  （默认 收益-回撤）排序择优；`python -m cyp.backtest.sweep`。最优配置可直接注入 Orchestrator。

- **回测/模拟/实盘三档统一**：`Backtester` 入场复用 Orchestrator 全管线
  （分析师→策略官→风控→PaperVenue），仅在回测层补按 bar 高低价触发的止损/止盈平仓，
  完成 round-trip；`HistoricalData` 按游标回放窗口快照。
- **绩效**：`compute_metrics` 纯函数——总收益/最大回撤/夏普/胜率/盈亏比。
- **CLI**：`python -m cyp.backtest.run`（合成历史，零密钥离线）。
- **仪表盘回测报告**：新增 `POST /api/backtest`，React 仪表盘可设置 symbol/bars/window/seed/
  drift/vol，展示绩效、净值曲线与交易明细。

### M4（部分）· 组合级风控 + 跨所聚合 + OKX 模拟交易 + 交易所适配层
- **组合级风控**：跨场所聚合持仓（`aggregate_positions` 失败隔离）→ `PortfolioView`
  计算总敞口/单标的/相关性簇同向净敞口；新护栏 `correlated_exposure`——相关性簇
  （majors/alt 聚类）内同向净敞口 ≤ 账户×`max_correlated_exposure`，避免押重相关篮子。
  编排器按 `risk_venues` 聚合，server/CLI 传入执行场所 + 注册表其它场所。
- **跨所行情聚合**：`MarketAggregator`——多场所报价、最优买卖场所、跨所价差(bps)
  （套利/异常线索，仅提示）；`GET /api/market`。
- **组合仪表盘**：`GET /api/portfolio` + 面板（净值/总敞口/相关性簇同向敞口对上限）。
- **仪表盘设置面板**：新增脱敏 `GET /api/settings`，React 仪表盘展示运行模式、审批模式、
  OKX Demo 配置状态、watchlist、LLM/场所凭据布尔状态、风控限制与 LiveGuard 校验结果。

- **交易所适配层** `venue/adapters.py`：把各家 ccxt 抹不平的差异（保护单参数、
  持仓/保证金模式）收敛到 adapter；CexVenue 通用流程不变，仅委托 configure_perp/
  entry_params/place_protective。BinanceAdapter（STOP_MARKET/TAKE_PROFIT reduce-only）、
  OkxAdapter（tdMode、set_leverage 带 mgnMode、stopLossPrice/takeProfitPrice）。
- **OKX Demo 模拟交易**：`sandbox`(set_sandbox_mode) + API passphrase；注册表注册 OKX venue，
  有 demo 凭据即可模拟下单否则只读；CLI `--venue okx`。假交易所离线验证参数差异。
  已完成 OKX Demo smoke test：私有余额接口、`BTC/USDT` 现货小额下单、条件保护单
  （止损/止盈）创建、查询、取消，以及测试仓位卖回清理。
- **OKX 接入硬化**：OKX `clientOrderId` 规范化；市价单返回 `filled=None` 时兜底解析；
  现货保护单不带 `reduceOnly`、永续保护单保留 reduce-only；保护单可按 OKX algo 单取消；
  CLI / FastAPI 生命周期关闭 ccxt client，减少资源泄漏噪音。

### M2 · CEX 实盘接入（离线部分完成，真实网络实操待做）
- **实盘下单**：`CexVenue` 现货+合约实盘（ccxt），幂等 `clientOrderId`、原生保护单
  （STOP/TP reduce-only）、perp 自动 `set_leverage`/`set_margin_mode`（逐仓）；
  **保护单失败即市价平裸仓**（有仓必有保护 fail-safe）。可注入假交易所离线测全覆盖。
- **实盘护栏**：`LiveGuard`——mode=live 需「有 Key + `CYP_LIVE_ACK=1` + Kill 未开」，
  否则退回只读（安全默认）；注册表据此决定 CexVenue 可否下单。
- **熔断真生效**：`PortfolioTracker` 用净值高水位/已实现亏损/下单时间戳驱动
  回撤·连亏·频率，接入风控引擎（此前这些字段恒为 0，熔断永不触发——已修复）。
- **告警**：`Alerter` 多 sink（控制台 + 可选 webhook），字段脱敏、sink 失败隔离；
  下单失败与熔断/Kill 否决触发告警。
- **风控看板**：`GET /api/risk` + 仪表盘面板（净值/回撤 meter/频率/连亏/实盘校验）。

### M1 · 合约永续（模拟盘）
- **风控**：合约专项硬护栏——逐仓强制（`force_isolated`）、维持保证金率下限（`min_margin_ratio`）；
  杠杆上限与爆仓缓冲复用既有规则。契约新增 `margin_mode`（默认 isolated）。
- **执行**：`PaperVenue` 合约按保证金记账（名义/杠杆），平仓结算浮盈 + 退保证金。
- **决策**：策略官支持 `instrument=perp`，杠杆由置信度决定（封顶 `max_leverage`）；`allow_perp` 开关（默认关）。
- **编排**：按现有永续名义计算维持保证金率喂给风控引擎。

### M0 · 骨架闭环（完全体）
- **契约**：`contracts/` pydantic 模型，7 步闭环全部数据结构，Decimal 计价，前后端单一真相。
- **风控引擎**：14 条确定性硬护栏（止损必填/单笔风险/单仓/总敞口/集中度/杠杆/爆仓缓冲/
  滑点/价格冲击/频率/连亏/回撤熔断/Kill Switch/对账冻结），一票否决 + 自动缩仓。
- **Venue 抽象**：`PaperVenue`（确定性撮合 + 幂等 + 入场挂保护单）、`CexVenue` 只读行情（Binance 参考）、注册表。
- **数据管线**：纯 Python 指标（SMA/EMA/RSI/MACD/ATR/BOLL）+ 合成/真实两种行情源（无密钥可跑）。
- **多智能体**：技术/衍生品/情绪/链上分析师 → 首席策略官 → 风控官 → 交易员 → 复盘官；
  规则为主、LLM 增强、失败隔离、静默降级。
- **LLM 层**：`ResilientLLM`（重试/退避/熔断/超时）+ 结构化输出（tool-use → pydantic 校验），异常统一降级。
- **编排 + 运行时**：7 步闭环 + 事件总线 + 检查点；启动对账（对账门冻结开仓）+ 机会扫描/持仓监控双循环。
- **审批门**：CLI + 仪表盘按钮 + 挂起-解决（Web 人在环），超时=拒绝（fail-safe）。
- **服务 + 仪表盘**：FastAPI REST + SSE + Kill Switch；自包含仪表盘（事件流/待审批/持仓，零构建）。
- **可观测**：结构化 JSON 日志（自动脱敏）+ trace/span + RunMetrics。

### 工程
- 无密钥、离线、确定性可端到端跑通；当前测试 180 passed。
- 参考交易所锁定 Binance；OKX 等推到 M4「多所」。

[未发布]: https://example.com/cyp-agent
