# 更新日志

遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/)。版本遵循语义化版本。

## [未发布]

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
