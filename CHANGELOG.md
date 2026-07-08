# 更新日志

遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/)。版本遵循语义化版本。

## [未发布]

### M4（部分）· OKX 模拟交易 + 交易所适配层
- **交易所适配层** `venue/adapters.py`：把各家 ccxt 抹不平的差异（保护单参数、
  持仓/保证金模式）收敛到 adapter；CexVenue 通用流程不变，仅委托 configure_perp/
  entry_params/place_protective。BinanceAdapter（STOP_MARKET/TAKE_PROFIT reduce-only）、
  OkxAdapter（tdMode、set_leverage 带 mgnMode、stopLossPrice/takeProfitPrice）。
- **OKX Demo 模拟交易**：`sandbox`(set_sandbox_mode) + API passphrase；注册表注册 OKX venue，
  有 demo 凭据即可模拟下单否则只读；CLI `--venue okx`。假交易所离线验证参数差异。

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
- 无密钥、离线、确定性可端到端跑通；测试 93 passed。
- 参考交易所锁定 Binance；OKX 等推到 M4「多所」。

[未发布]: https://example.com/cyp-agent
