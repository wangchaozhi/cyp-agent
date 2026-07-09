# 迭代路线图 · cyp-agent

> 演进主线：**先立护栏，再放智能；先跑通模拟，再碰真钱；先 CEX，再链上；先单所，再组合。**
> 每个里程碑独立可交付、可演示。验收标准即 [KICKOFF.md §10 Definition of Done](KICKOFF.md) 的具体化。

## 里程碑总览

| 里程碑 | 主题 | 核心增量 | 风险等级 |
| --- | --- | --- | --- |
| M0 | 骨架闭环 | 模拟盘端到端 7 步 | 无（零资金） |
| M1 | 合约永续 | 资金费/持仓/杠杆/爆仓纳入 | 无（仍模拟） |
| M2 | CEX 实盘 | 真实下单 + Kill Switch | ★★★ |
| M3 | 链上 DeFi | 钱包/DEX + 授权护栏 + MEV | ★★★★ |
| M4 | 多所 + 组合 | 跨所行情 + 组合级风控 | ★★★ |
| M5 | 策略库 + 回测 | 历史数据 + 回测引擎 | ★ |
| M6 | 进阶自动化 | 策略化自动审批 + 记忆增强 | ★★★★ |

---

## M0 · 骨架闭环（MVP）

**目标**：PaperVenue 上端到端跑通 采集→分析→决策→风控→审批→执行→复盘，无任何外部密钥可演示。

功能条目（进度：`x`=完成 `~`=部分）：
- [x] 契约 `contracts/models.py`（7 步全部数据结构，Decimal 计价）
- [x] `Venue` 抽象 + `PaperVenue`（确定性撮合 + 幂等 + 入场挂保护单）+ `CexVenue` 只读行情
- [x] 数据管线：纯 Python 指标（SMA/EMA/RSI/MACD/ATR/BOLL）+ 合成/真实两种行情源
- [x] 分析师团：technical/derivatives（真实规则）、sentiment/onchain（缺数据降级）
- [x] 首席策略官（规则定数字 + LLM 仅润色论述）
- [x] **确定性风控引擎**：14 条硬护栏（单笔风险/止损必填/回撤熔断/…），25 单测
- [x] 风控官（LLM 软评审只收紧 + 无 LLM 透传）
- [x] 审批门（CLI 交互 + 仪表盘按钮）+ `examples/run.py`
- [x] 交易员 → PaperVenue 模拟成交 + 复盘官
- [x] `orchestrator` 串联 7 步 + `events` 总线 + `memory` 检查点 + `ResilientLLM`
- [x] `apps/server`（FastAPI REST + SSE）+ `apps/web` 仪表盘（事件流/待审批/持仓/KillSwitch）
- [x] 运行时（[RUNTIME.md](RUNTIME.md)）：入场挂保护单 + 启动对账（对账门冻结开仓）+ 扫描·监控双循环
- [x] 可观测：结构化 JSON 日志脱敏 + trace/span（每步一 span）+ RunMetrics

**验收**：✅ 全部达成——无密钥离线跑完整闭环（`python -m cyp.examples.run`，含 `--loop N` 运行时）；
风控用例（无止损/超仓/Kill Switch/对账冻结）过 CI；仪表盘全流程可见。**M0 完全体 done。**

---

## M1 · 合约永续

**目标**：把 U/币本位永续纳入分析与风控，仍在模拟盘。（进度：`x`=完成 `~`=部分）

- [x] `PaperVenue` 合约模拟：保证金记账（名义/杠杆）+ 爆仓价估算 + 平仓浮盈结算
- [x] `CexVenue` 合约行情 + 资金费/OI 只读读取（`fetch_funding_rate` 失败隔离，聚合器/API 消费）
- [x] 数据管线：合成源提供资金费/OI/多空比；衍生品分析师消费
- [x] 衍生品分析师：资金费拥挤度、多空失衡信号
- [x] 策略官支持 `instrument=perp` + 由置信度决定杠杆（封顶 max_leverage）
- [x] 风控引擎 §2.2 合约专项：杠杆上限、爆仓缓冲、**维持保证金、逐仓强制**
- [x] 仪表盘补：持仓表爆仓价/保证金/资金费列 + 逐仓/全仓标注；风险面板保证金健康度
      （`/api/positions` 补 `liq_price/margin_used/funding_rate`，`/api/risk` 补 `margin_ratio`）

**验收**：✅ 模拟盘跑通一笔永续（`allow_perp` + `test_orchestrator_perp_end_to_end`）；
杠杆/爆仓缓冲/逐仓/维持保证金护栏在 CI 被验证否决（`test_risk_perp.py`）。**M1 done。**

---

## M2 · CEX 实盘接入 ★★★

**目标**：真实资金、小额、最小权限、强熔断。这是第一次碰真钱，谨慎至上。**参考交易所 = Binance**（ccxt 最成熟、Testnet 齐全）；OKX 等推到 M4「多所」。

- [x] `CexVenue` 实盘下单（现货 + 合约），幂等 `clientOrderId`（假 ccxt 离线测全覆盖）
- [x] **Binance 适配层**：原生保护单（STOP/TP reduce-only）、`set_leverage`/`set_margin_mode`（逐仓）
- [x] **真实网络验收（以 OKX Demo 替代 Binance Testnet，后者不采用）**：`python -m cyp.tools.okx_smoke`
      联网跑通 配置校验→余额→现货下单（带止损/止盈保护单）→幂等重放→撤保护单→平仓清理→增量对账，全绿
- [x] 前置校验：`LiveGuard`——有 Key + `CYP_LIVE_ACK=1` + Kill 未开，否则退回只读（提现权限/IP 白名单交易所侧）
- [x] **交易所侧原生保护单** + **下保护失败即市价平裸仓**（fail-safe，已测）；对账门冻结开仓（M0 已具）
- [x] Kill Switch 全链路 + 熔断规则真实生效（组合账本驱动回撤/连亏/频率，已测触发）
- [x] 审批门仪表盘版（按钮批准/拒绝/修改）+ 挂起-解决（M0 已具）
- [x] 实时风控看板：`/api/risk` + 仪表盘面板（净值/回撤/频率/连亏/实盘校验）
- [x] 告警：下单失败/熔断 → Alerter（控制台 + 可选 webhook，脱敏）
- [ ] 灰度：单标的小额 + `paper` 影子对照（属真实网络实操阶段）

**验收**：✅ 离线部分达成——实盘下单/保护单/保护失败平裸仓、LiveGuard 只读门、熔断真实触发、
风控看板、告警均已实现并测试（`test_cex_trading` / `test_live_alerts` / `test_portfolio`）。
✅ 真实网络部分以 OKX Demo smoke（`cyp.tools.okx_smoke`）验收通过。
⏳ 剩小额主网一笔 + `paper` 影子对照（涉及真金，留实操阶段）。

---

## M3 · 链上 DeFi ★★★★

**目标**：EVM 系钱包 + DEX swap，先模拟/测试网，再主网小额。风险最高，护栏最重。

- [x] `OnchainVenue`（`venue/onchain.py`）：web3 惰性导入 + 可注入 mock client；
      `preflight`（gas 报价 + 价格冲击 + 授权检查）；「精确额度 approve → swap」两步执行
- [x] **隔离签名器**（`onchain/signer.py`）：本地加密 keystore（eth-account）；私钥不落日志/不进 LLM；
      KMS/硬件签名器留接口
- [~] 链上数据管线：`OnchainDataSource` stub（无 RPC 降级 None）；聪明钱/巨鲸/池深等真实数据源待接
- [~] 链上分析师：降级骨架在位（缺数据自动降级），真实数据接入后激活
- [x] 风控补 §2.3 链上专项：禁无限授权、合约白名单、最小池 TVL、价格冲击（已接 preflight）、
      gas 上限、MEV 防护（要求私有内存池路由）
- [x] nonce 管理 + tx 确认跟踪 + revert 处理 + 幂等去重；`reconcile_onchain` nonce 对齐 /
      pending tx 归位（[RUNTIME.md §3.3](RUNTIME.md)）
- [x] 链上持仓保护：仪表盘持仓表标注「链上·保护依赖监控存活」；监控循环止损（PositionMonitor 覆盖）
- [~] 仪表盘补：链上持仓/tx hash 已展示；授权额度/gas/待确认 tx 明细面板待真实 RPC 接入后补

**验收**：✅ mock client 离线部分——swap 两步执行/幂等/revert 处理/nonce 对账/链上五条护栏
全部单测通过（`test_onchain.py`）。⏳ 真实测试网 swap + 主网小额验证需 RPC 与测试网资金，留实操阶段。

---

## M4 · 多所 + 组合

**目标**：跨所行情聚合 + 组合级风控。

- [x] 接入第二家 CEX（**OKX**）：交易所适配层（tdMode/mgnMode/统一保护单）+ **OKX Demo 模拟交易**
      （sandbox + passphrase），`--venue okx` 可用；假交易所离线测参数差异，并已实测 OKX Demo
      `BTC/USDT` 现货小额下单、条件保护单创建/取消与测试仓清理
- [x] Venue 注册表多 CEX 并存（paper/binance/okx）；**跨所行情聚合**（最优买卖场所 + 跨所价差）`/api/market`
- [x] 组合级风控：跨场所聚合持仓 → 总敞口 + 单标的集中度 + **相关性簇同向净敞口护栏**
- [x] 策略官感知现有组合：`Strategist.run` 接收聚合持仓——同标的同向已有仓 → flat；
      相关簇同向敞口超 80% 上限 → 按剩余额度缩量或 flat（规则路径，硬护栏之外的主动规避）
- [x] 组合仪表盘：`/api/portfolio` + 面板（净值/总敞口/相关性簇同向敞口对上限）+
      `by_symbol` 敞口热力图（纯 CSS 色阶）
- [x] 跨所价差/资金费套利线索（仅提示，不自动执行）：聚合器 `funding_rates()` + `arb_hints()`
      （价差 bps 超阈 / 跨所资金费差），`/api/market` 暴露 + 前端 MarketPanel

**验收**：✅ 多所同时运行；组合相关性敞口超限被否决；组合看板/热力图/套利线索可用。
OKX Demo smoke 已脚本化（`cyp.tools.okx_smoke`）并联网通过。**M4 done。**

---

## M5 · 策略库 + 回测

**目标**：把「策略」显式化、可参数化、可回测择优。

- [x] 历史回放：`HistoricalData` 按游标返回窗口快照（合成历史 + `OhlcvArchive` 真实归档：
      ccxt 分页拉取 OHLCV 落 SQLite 增量缓存，`--data cex` / `POST /api/backtest data=cex`）
- [x] 回测引擎：`Backtester` 入场**复用同一套** 分析师→策略官→风控（Orchestrator+PaperVenue），
      按 bar 高低价触发止损/止盈平仓，完成 round-trip
- [x] 绩效评估：总收益/最大回撤/夏普/胜率/盈亏比（`compute_metrics` 纯函数，单测）
- [x] 策略参数化：`StrategyConfig` 打包（权重/入场阈值/ATR 止损止盈倍数/单笔风险）+
      `grid()` 扫参 + `sweep()` 按目标函数排序择优（`python -m cyp.backtest.sweep`）
- [x] 反过拟合统计基础件：PSR / Deflated Sharpe / MinTRL + walk-forward / purged K-fold / PBO
      （详见 [QUANT.md](QUANT.md) 与 [quant/stats.md](quant/stats.md)）
- [x] 复盘经验与回测结果打通：`Backtester` 挂接 `MemoryStore`，`BacktestReport.lessons`
      汇总复盘经验；strategist/risk_officer LLM 提示词消费 `ctx.lessons`（规则路径不变）
- [x] 仪表盘：`POST /api/backtest` + 回测报告页（参数、绩效、净值曲线、交易明细、lessons、
      数据源 synthetic/cex 可选）

**验收**：✅ `python -m cyp.backtest.run` 对合成/真实历史跑出可复现回测报告；入场逻辑与实盘
**同一份代码**（Orchestrator），「回测/模拟/实盘」无逻辑分叉；仪表盘可直接运行回测并查看报告。**M5 done。**

---

## M6 · 进阶自动化 ★★★★

**目标**：在护栏内引入策略化自动审批，向「更少人工」演进——但永不移除硬护栏与 Kill Switch。

- [x] `CYP_APPROVAL=auto`：`PolicyApprovalGate`——满足「symbol 白名单 + risk_score < 阈值 +
      金额 < 上限」自动批准，否则委托内层人工门；CLI 与 FastAPI 均已接线
- [x] 长期记忆增强：`MemoryStore` 迁 SQLite，lessons 带 symbol 元数据，按符号 + 词元重合度
      打分检索最相关 N 条注入上下文（轻量检索，不引向量库；旧 JSON 自动迁移）
- [x] 定时巡检：`PositionMonitor` 增强——止损逼近/爆仓逼近/异常波动（EWMA σ 突破）/
      保证金率告警走 Alerter；FastAPI 可选启动 RuntimeEngine（`CYP_RUNTIME=1`）
- [~] 多操作员：审批 `operator` 透传 + 审计事件带操作者；多账户隔离与权限体系待后续
- [x] 更细的告警与 SLO：`RunMetrics` 增加审批时延/滑点分布（分桶）/下单成功率，
      `GET /api/metrics` 暴露 + OverviewStrip 展示

**验收**：✅ auto 模式仅对白名单小额自动放行，超阈仍转人工（`test_policy_gate.py`）；
关闭 auto 与 Kill Switch 随时可用；记忆检索持久化且可观测（`test_memory.py`）。

---

## 横切事项（贯穿所有里程碑）

- **测试**：每个 Agent/规则单测；无密钥端到端测试为 CI 门禁；实盘特性必先过 paper。
- **可观测**：新特性必带 trace/span/日志与仪表盘可见状态。
- **文档**：每个里程碑更新对应 docs/ 章节 + CHANGELOG。
- **契约**：跨层数据结构一律走 `contracts/`，TS 类型同步。
- **安全**：任何触及资金/私钥的改动，过 RISK.md 清单复核。

## 量化深化线（Q1–Q4，与 M 里程碑并行）

工程与市场覆盖是 M 线；**数学金融内核**的升级是 Q 线，详见 [QUANT.md](QUANT.md)：
- **Q1** 反自欺 + 波动率内核：walk-forward / Deflated Sharpe / PBO 防过拟合；EWMA/GARCH 波动率；VaR/CVaR 护栏；实测协方差替换静态相关性聚类。当前已完成 walk-forward、purged K-fold、PBO、PSR/DSR/MinTRL、EWMA 波动率、波动率目标仓位、Historical VaR/CVaR 与 CVaR 硬护栏基础件。
- **Q2** 更优仓位 + regime + 组合优化：分数 Kelly / 波动率目标；HMM 状态识别；HRP / 风险平价。
- **Q3** 统计套利 + 微观结构：协整配对 + 卡尔曼；订单簿失衡；Almgren-Chriss 最优执行。
- **Q4** 进阶：EVT / Copula / meta-labeling（严格 OOS 把关下）。

> 核心观点：加密里**回测严谨性 > 花哨 alpha**，Q1 的反过拟合套件优先级最高。

## 演进方向（M6 之后的开放议题）

- 全自动无人值守（需更强护栏、影子对照、异常自愈成熟后）
- 更多资产类别 / 更多链 / L2
- 策略市场化（多策略并行 + 资金分配）
- 模型多样化（多 LLM 投票、专用小模型做指标解读）
