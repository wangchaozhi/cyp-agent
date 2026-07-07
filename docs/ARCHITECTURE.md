# 架构设计 · cyp-agent

## 设计目标

1. **风控优先于智能**：确定性硬护栏（`risk/`）先于任何 LLM 决策执行并拥有一票否决权，LLM 只能在护栏内建议。
2. **场所可插拔**：CEX（现货+合约）与链上 DeFi 统一到一个 `Venue` 接口，新增交易所/链只实现接口 + 注册一行。
3. **无密钥可降级**：没有交易所 Key、没有 LLM Key，也能用 `PaperVenue` + 规则模板信号端到端跑通。
4. **契约单一来源**：`contracts/` 的 pydantic 模型是前后端唯一真相，React 类型由其生成。
5. **生产级韧性**：重试/熔断/检查点/任务租约/幂等下单/可观测/成本护栏内建（复用 prod-agent 四大支柱）。

## 总览

```
┌───────────────────────────── apps/web (React + Vite) ──────────────────────────────┐
│  信号流   待审批卡片   持仓/PnL   风控看板   系统状态   Kill Switch                  │
└───────────────────────────────┬─────────────────────────────────────────────────────┘
                                 │ REST + SSE（契约类型来自 packages/shared，由 pydantic 生成）
┌────────────────────────────────▼──────────────── apps/server (FastAPI) ─────────────┐
│  routes/  snapshots · proposals · approvals · positions · events(SSE) · killswitch   │
└────────────────────────────────┬─────────────────────────────────────────────────────┘
                                 │
┌────────────────────────────────▼──────────────── core/cyp ──────────────────────────┐
│  orchestrator.py  ── 编排 7 步闭环，逐步落检查点、发事件                              │
│                                                                                       │
│  agents/     ┌──────────────────────────────────────────────────────────────────┐   │
│              │ ① 分析师团（并行）  技术面 · 衍生品 · 情绪 · 链上                   │   │
│              │ ② 首席策略官        合成 TradeProposal                            │   │
│              │ ③ 风控官            LLM 软评审（在硬护栏之上）                     │   │
│              │ ④ 交易员            审批后执行编排                                 │   │
│              │ ⑤ 复盘官            归因 + 经验沉淀（反馈闭环）                     │   │
│              └──────────────────────────────────────────────────────────────────┘   │
│                                                                                       │
│  risk/       确定性风控引擎（★非 LLM，一票否决）：仓位/回撤/杠杆/敞口/授权/冷静期     │
│  approval/   人工审批门：pending 队列 · 超时策略 · 全审计                            │
│  venue/      统一场所抽象   CexVenue(ccxt) │ OnchainVenue(web3+DEX) │ PaperVenue     │
│  data/       数据管线   行情/K线/订单簿 · 资金费/OI · 链上流向 · 情绪                │
│  llm/        ResilientLLM   anthropic │ mock（缺 Key 降级）                          │
│  execution/  订单生命周期 + 幂等 + 链上签名器（隔离，永不落盘）                       │
│  portfolio/  持仓/账本/盈亏     memory/  检查点(aiosqlite WAL)                        │
│  observability/  trace · metrics · JSON 日志（脱敏）    events.py  事件总线→SSE       │
│  contracts/  ★ pydantic 模型（前后端契约单一来源）                                   │
└───────────────────────────────────────────────────────────────────────────────────────┘
```

## 分层职责

| 层 | 目录 | 职责 | 关键约束 |
| --- | --- | --- | --- |
| 契约 | `contracts/` | 定义所有跨层数据结构 | 唯一真相；改动需同步 TS |
| 编排 | `orchestrator.py` | 串联 7 步、落检查点、发事件、驱动反馈闭环 | 不含业务判断，只调度 |
| 智能体 | `agents/` | LLM 驱动的分析/决策/评审/复盘 | 纯模块、依赖注入、必带降级 |
| 风控 | `risk/` | 确定性硬护栏 + 否决 | **非 LLM**、纯函数、全单测 |
| 审批 | `approval/` | 人在环关卡 | 每笔真实下单必经；全审计 |
| 场所 | `venue/` | 统一 CEX/链上/模拟接口 | 可插拔；下单幂等 |
| 数据 | `data/` | 行情/衍生品/链上/情绪采集与指标 | 缺源降级不阻断 |
| 执行 | `execution/` | 订单生命周期、签名 | 幂等；私钥隔离 |
| 平台 | `llm/ memory/ observability/ events.py` | 韧性/持久/可观测/事件 | 复用 prod-agent 支柱 |

## 契约单一来源

`contracts/models.py`（pydantic v2）定义 7 步之间流动的全部数据结构：

```python
class MarketSnapshot(BaseModel):      # 采集产物：某标的的多维快照
    symbol: str
    venue: str
    ts: datetime
    ohlcv: list[Candle]
    orderbook: OrderBook | None
    derivatives: DerivativesData | None   # 资金费/OI/爆仓/基差（合约）
    onchain: OnchainData | None           # 聪明钱流向/DEX 流动性/持有分布
    sentiment: SentimentData | None       # 恐贪/新闻情绪/社媒热度

class AnalystReport(BaseModel):       # 每位分析师产物
    agent: str                         # technical / derivatives / sentiment / onchain
    stance: Literal["bullish","bearish","neutral"]
    confidence: float                  # 0..1
    signals: list[Signal]              # 结构化信号
    rationale: str                     # 中文说明
    degraded: bool                     # 是否因缺数据降级

class TradeProposal(BaseModel):       # 首席策略官产物
    symbol: str; venue: str
    side: Literal["long","short","flat"]
    instrument: Literal["spot","perp"]
    size_quote: Decimal                # 计价币规模
    leverage: float                    # 现货=1
    entry: PricePlan                   # 市价/限价/区间
    stop_loss: Decimal                 # ★ 必填，无止损直接被风控否决
    take_profit: list[Decimal]
    confidence: float
    thesis: str; supporting_reports: list[str]

class RiskAssessment(BaseModel):      # 风控引擎 + 风控官产物
    verdict: Literal["approved","downsized","rejected"]
    hard_violations: list[str]         # 触发的确定性规则（否决原因）
    adjusted_size_quote: Decimal | None
    llm_notes: str
    risk_score: float                  # 0..1

class ApprovalDecision(BaseModel):    # 人工审批门产物
    decision: Literal["approve","reject","modify"]
    modified: TradeProposal | None
    operator: str; ts: datetime; note: str

class OrderIntent(BaseModel): ...     # 交易员 → Venue 的下单意图（含幂等 client_id）
class ExecutionResult(BaseModel): ... # 成交结果（成交价/量/手续费/滑点/订单状态）
class TradeReview(BaseModel): ...     # 复盘官产物（归因/评分/经验条目）
```

TS 类型生成：`pydantic → JSON Schema → quicktype/datamodel` 产出 `packages/shared/`，仪表盘只 import 这里。

## Venue 统一抽象（本架构最关键的设计）

CEX 与链上的执行语义差异极大，但对上层「交易员」必须长得一样。统一接口：

```python
class Venue(Protocol):
    id: str
    kind: Literal["cex", "onchain", "paper"]
    capabilities: VenueCaps            # 支持现货?合约?限价?保证金模式?
    def is_configured(self) -> bool: ...
    async def fetch_market(self, symbol) -> MarketSnapshotPart: ...
    async def positions(self) -> list[Position]: ...
    async def balances(self) -> Balances: ...
    async def place(self, intent: OrderIntent) -> ExecutionResult: ...   # 幂等
    async def cancel(self, order_id) -> None: ...
    async def preflight(self, intent) -> PreflightReport: ...            # 下单前估算
```

| 实现 | 底座 | 现货 | 合约 | 特有关注点 |
| --- | --- | --- | --- | --- |
| `CexVenue` | ccxt | ✅ | ✅ 永续 | **原生保护单**、**持仓/保证金模式**、资金费、精度/最小下单量、限频 |
| `OnchainVenue` | web3.py + DEX 路由(1inch/Jupiter) | ✅ swap | — | gas、滑点/价格冲击、token 授权、nonce、MEV、私有内存池 |
| `PaperVenue` | 内存撮合 | ✅ | ✅ | 零密钥；用真实行情喂价、确定性滑点模型；M0 默认 |

`preflight()` 是统一的「下单前体检」：CEX 检查精度/余额/限频，链上估算 gas + 价格冲击 + 检查授权额度。风控引擎消费 preflight 结果做最终硬校验。

**新增一个场所**：实现 `Venue` → `registry.register(...)` 一行 → 仪表盘的场所选择、配置状态、能力展示全部自动生效（数据来自 `GET /api/venues`）。对标 game-asset-forge 的 provider 注册表。

### CEX 适配点（每家交易所必做，参考实现 = Binance）

ccxt 统一了行情/下单，但下列细节各家不同、**统一不了**，必须按交易所写小适配器并单独测试。它们直接决定 [RUNTIME.md](RUNTIME.md) 的「有仓必有保护」能否落地：

| 适配点 | 说明 | ccxt 能否抹平 |
| --- | --- | --- |
| **原生保护单** | 止损/OCO/reduce-only/bracket 的参数与附带止盈止损方式各家不同 | ❌ 需 per-venue 适配 |
| **持仓模式** | 单向 vs 双向（Binance `dualSidePosition` / OKX position mode） | ❌ |
| **保证金模式** | 全仓 vs 逐仓切换方式 | ❌ |
| 精度/最小下单量/手续费 | 数值与规则各异，影响 preflight | ⚠️ 部分 |
| 限频 | 权重与窗口各异 | ⚠️ 部分 |
| WebSocket 流 | 行情/成交推送格式与限制 | ⚠️ 部分 |
| testnet/demo | Binance Futures/Spot Testnet；OKX Demo Trading | — |

> **参考实现选定 Binance**：ccxt 对其最成熟、Testnet 齐全，最适合先把「实盘下单 + 原生保护单 + 对账」这套硬骨头啃透。设计上 `CexVenue` 以 ccxt id 参数化 + 一个按交易所分派的「保护单/持仓模式适配层」；第二家（OKX 等）在 M4「多所」基本是「填适配层 + 一轮针对性测试」，不是重写。

## 多智能体编排与反馈闭环

编排在 `orchestrator.py`，每个 Agent 是**显式注入依赖的纯模块**（便于单测/替换）。核心闭环见 [KICKOFF.md §5](KICKOFF.md)。两条反馈闭环：

- **风控否决闭环**：`风控引擎/风控官 verdict=rejected` → 否决理由注入首席策略官 → 重议（≤ `maxRetries`）。对标 game-asset-forge 的「审查官打回提示词工程师」。
- **复盘经验闭环**：`复盘官` 产出的 `TradeReview.lessons` 写入 `memory/`，下一轮作为上下文注入分析师/策略官（轻量长期记忆）。

关键决策（沿用两参照项目的工程哲学）：
- **失败隔离**：单个分析师失败只记日志、标 `degraded=true`，不阻断其它维度；全部失败才判该轮失败。
- **静默降级**：任一 Agent 的 LLM 调用异常 → 回退规则模板；数据源缺失 → 该维度跳过。系统永不因 LLM/单一数据源阻断。
- **人工兜底**：审批门是最终防线，即便前面全对，人仍可一票拒绝。

## 降级矩阵

| 缺失 | 降级行为 | 仍可跑通? |
| --- | --- | --- |
| 交易所 API Key | 用 `PaperVenue` + CEX 只读公共行情 | ✅ |
| `ANTHROPIC_API_KEY` | 所有 Agent 回退规则模板信号 | ✅ |
| 情绪数据源 | 情绪分析师标 degraded、权重降为 0 | ✅ |
| 链上 RPC | 链上分析师跳过；`OnchainVenue` 不可用但不影响 CEX | ✅ |
| 数据库不可写 | 内存态运行 + 告警（失去断点续跑能力） | ✅（有损） |

> 铁律：**任何单点失效都降级而非崩溃**，且降级路径是 CI 门禁（无密钥端到端测试必过）。

## 生产四大支柱（复用 prod-agent）

**可靠性** — `ResilientLLM` 仅重试瞬态错误（429/5xx/超时），指数退避+抖动，熔断器；LLM `stop_reason` 全语义处理。每步落检查点，`resume(run_id)` 断点续跑。**任务租约**防多进程重复下单（同一 `run_id` 互斥）。**下单幂等**：`OrderIntent.client_id` 作为交易所 `clientOrderId` / 链上 nonce 去重键，崩溃重放不重复成交。

**可观测性** — 每轮一个 `trace_id`，每次 LLM/下单/preflight 一个 span；JSON 结构化日志可直接进 ELK/Loki；`metrics.snapshot()` 导出信号数、审批通过率、成交/滑点、PnL、token 用量与延迟分布；日志自动脱敏 `api_key/private_key/token`。

**成本控制** — 迭代次数、总 token、美元成本、墙钟四重硬上限，触发即优雅终止；默认开启提示词缓存（system/tools/最新消息打断点）；分析师团的只读 LLM 调用批量并行降延迟。

**安全** — 工具白名单（未注册即拒）；READ/WRITE/EXECUTE 三级权限，**下单属 EXECUTE 必经审批门**；参数下单前 JSON Schema 校验；交易所 Key 只从 env 读、禁提现权限；**链上私钥走隔离签名器**（本地 keystore/KMS/硬件），永不落盘、永不进 LLM 上下文、永不出现在日志。详见 [RISK.md](RISK.md)。

## 数据流时序（一次完整交易）

```
web ──POST /run(symbol)──▶ server ──▶ orchestrator
  orchestrator: ① data.* 并发拉取 → MarketSnapshot        │ span: collect
              → ② agents 并行 → [AnalystReport...]         │ spans: analyze.*
              → ③ strategist → TradeProposal               │ span: strategize
              → ④ risk.engine 硬校验 →(pass)→ risk_officer  │ span: risk
                   └─(reject)→ 反馈→重议  ↺
              → 发事件 proposal_ready ──SSE──▶ web 弹出待审批卡片
  operator ──POST /approvals/{id}(approve)──▶ approval.gate → ApprovalDecision
  orchestrator: ⑥ trader → venue.preflight → venue.place    │ span: execute
              → ExecutionResult ──SSE──▶ web 更新持仓/PnL
  (持仓平仓后) ⑦ reviewer → TradeReview → memory.lessons     │ span: review
  每步 memory.checkpoint(run_id)  ·  全程 events → SSE
```

## 运行时驱动与恢复（三条循环 + 保护 + 对账）

上面的时序是「一次 run」的内部流程；但 7×24 市场里**谁驱动 run、持仓怎么被保护、崩溃后怎么恢复**同样是架构的一部分。详见 [RUNTIME.md](RUNTIME.md)，要点：

- **三条循环**：`机会扫描`（找新仓）+ `持仓监控`（常驻高频，盯已有仓，可触发防御性减仓/平仓）+ `Watchdog`（心跳/对账/告警）。触发（定时+事件）产出 `RunRequest` → 受控并发队列（同 symbol 租约互斥）→ orchestrator。
- **止损独立于进程存活**：CEX 入场成交后**立即挂交易所侧原生保护单**（bracket/OCO/reduce-only），下保护失败则立即平掉裸仓；链上无原生止损，改为监控循环 + 更严护栏（更小仓位/更大缓冲）。**有仓必有保护**。
- **对账优先于决策**：启动/恢复/周期性都与交易所/链上真实状态对账（真相在远端），**对账未完成冻结新开仓**（`state=RECONCILING`），仅允许减仓/平仓/补保护。
- **方向相反的 fail-safe**：开仓审批超时=拒绝；护仓动作超时=自动保护（平仓/补挂），因为此处「不动」才是危险。

## 与两个参照项目的关系

- 借鉴 **game-asset-forge**：多智能体流水线 + 反馈闭环、Provider/Venue 注册表、SSE 实时、契约单一来源、无密钥可跑（mock 引擎 ↔ PaperVenue）。
- 借鉴 **prod-agent**：ResilientLLM、检查点/续跑、任务租约、四大支柱、三级权限与审批。
- 新增本项目独有：**统一 CEX+链上 Venue 抽象**、**确定性风控引擎（一票否决）**、**人工审批门**——这三者是「加密货币 + 半自动 + 真金白银」场景的核心。
