# 开工文档 · cyp-agent

> 本文档是项目的「唯一入口 + 施工蓝图」。任何人接手，读完这一篇即可开工。
> 配套阅读：架构细节看 [ARCHITECTURE.md](ARCHITECTURE.md)，多智能体规格看 [AGENTS.md](AGENTS.md)，运行时驱动/止损落地/对账看 [RUNTIME.md](RUNTIME.md)，风控红线看 [RISK.md](RISK.md)，迭代计划看 [ROADMAP.md](ROADMAP.md)。

---

## 1. 一句话定义

一个**半自动**的加密货币多智能体交易助手：自动完成「行情/情报采集 → 多维分析 → 交易决策 → 风控校验」，**在人工审批后**执行下单，并自动复盘沉淀经验。覆盖 **CEX 现货 + 合约永续 + 链上 DeFi**。

## 2. 为什么是「半自动 + 风控优先」

加密市场 7×24、高波动、不可逆（链上交易尤甚）。全自动实盘的尾部风险（模型幻觉、数据污染、交易所故障、合约钓鱼）足以在一次错误中清空账户。因此本项目的第一性原理是：

> **智能负责「找机会」，护栏负责「不出事」，人负责「按下最后一个按钮」。**

- **确定性风控引擎**（非 LLM）拥有一票否决权，任何提案违反硬护栏 → 直接毙掉，不进审批。
- **人工审批门**是每一笔真实下单的必经关卡（半自动的定义）。
- 全流程默认跑在 **PaperVenue 模拟盘**，实盘是需要显式开启、且最小权限的「特权模式」。

这个起点最容易做到「完美且不炸」，且能平滑演进到全自动（把审批门替换为策略化自动批准 + 更强护栏）。

## 3. 范围（Scope）

### 3.1 In Scope

| 维度 | 范围 |
| --- | --- |
| 市场 | CEX 现货、CEX U/币本位永续合约、链上 DeFi（EVM 系 DEX + 钱包） |
| 场所 | 通过统一 **Venue 抽象**：`CexVenue`(ccxt) / `OnchainVenue`(RPC+DEX) / `PaperVenue`(模拟) |
| 情报 | 行情/K线/订单簿、资金费率/持仓量/爆仓、链上流向/聪明钱/DEX 流动性、社媒/新闻/宏观 |
| 智能体 | 分析师团（技术/衍生品/情绪/链上）→ 首席策略官 → 风控官 → 交易员 → 复盘官 |
| 执行 | 人工审批后下单；订单幂等、生命周期跟踪、部分成交处理；链上含 gas/滑点/授权/MEV 防护 |
| 交付 | Python 核心 + React 实时仪表盘（信号流、待审批、持仓、PnL、风控看板） |

### 3.2 Out of Scope（明确不做，避免范围蔓延）

- ❌ 全自动无人值守实盘（M2 后作为可选特性，非默认）
- ❌ 高频/做市/套利（架构不排斥，但非本线目标；延迟不作极致优化）
- ❌ 为他人托管资金、任何形式的资管产品（合规红线）
- ❌ 自研撮合/自建交易所
- ❌ 提现/转账自动化（永久禁用，见 RISK.md）

## 4. 目标技术栈与目录结构

**核心**：Python 3.11+ · ccxt · web3.py/eth-account · pandas · pandas-ta · pydantic v2 · FastAPI · aiosqlite · anthropic SDK
**仪表盘**：React 18 · Vite · TypeScript（契约类型由 pydantic 生成）

```
cyp-agent/
├─ pyproject.toml              # 核心包 cyp，extras: [anthropic] [onchain] [dev]
├─ .env.example               # 全部配置项的单一参考
├─ docker-compose.yml         # server + web + (可选) 本地 anvil 链
├─ docs/                      # 本文档集
├─ core/
│  └─ cyp/
│     ├─ config.py            # Settings（env 加载）+ RiskConfig + BudgetConfig
│     ├─ orchestrator.py      # 流水线编排（对标 game-asset-forge/pipeline.ts）
│     ├─ contracts/           # ★ 契约单一来源（pydantic 模型）
│     │  └─ models.py         # MarketSnapshot / AnalystReport / TradeProposal ...
│     ├─ agents/              # 多智能体（每个是显式注入依赖的纯模块）
│     │  ├─ base.py
│     │  ├─ technical.py      #   技术面分析师
│     │  ├─ derivatives.py    #   衍生品分析师（资金费/持仓/基差）
│     │  ├─ sentiment.py      #   情绪分析师
│     │  ├─ onchain.py        #   链上分析师（聪明钱/流动性/持有分布）
│     │  ├─ strategist.py     #   首席策略官（决策合成）
│     │  ├─ risk_officer.py   #   风控官（LLM 评审，护栏之上的软审查）
│     │  ├─ trader.py         #   交易员（执行编排）
│     │  └─ reviewer.py       #   复盘官（归因 + 经验沉淀）
│     ├─ risk/                # ★ 确定性风控引擎（非 LLM，一票否决）
│     │  ├─ engine.py
│     │  └─ rules.py          #   仓位/回撤/杠杆/敞口/滑点/授权/冷静期
│     ├─ data/                # 数据管线
│     │  ├─ market.py         #   ccxt 行情/K线/订单簿
│     │  ├─ derivatives.py    #   资金费/OI/爆仓
│     │  ├─ onchain.py        #   链上数据（RPC / 数据商 API）
│     │  ├─ sentiment.py      #   新闻/社媒/恐贪指数
│     │  └─ indicators.py     #   pandas-ta 指标计算
│     ├─ venue/               # ★ 统一交易场所抽象
│     │  ├─ base.py           #   Venue 接口
│     │  ├─ cex.py            #   CexVenue（ccxt，现货+合约）
│     │  ├─ onchain.py        #   OnchainVenue（web3 + DEX 路由）
│     │  ├─ paper.py          #   PaperVenue（模拟盘，零密钥降级）
│     │  └─ registry.py       #   注册表（新增场所改一行）
│     ├─ llm/                 # LLM 适配层（复用 prod-agent 设计）
│     │  ├─ base.py           #   ResilientLLM：重试/退避/熔断/流式/缓存
│     │  ├─ anthropic.py
│     │  └─ mock.py           #   零密钥降级
│     ├─ approval/            # 人工审批门
│     │  └─ gate.py           #   pending 队列 + 超时策略 + 审计
│     ├─ execution/           # 订单生命周期 + 幂等
│     │  ├─ executor.py
│     │  └─ signer.py         #   链上签名器（隔离，永不落盘）
│     ├─ portfolio/           # 持仓/账本/盈亏
│     ├─ memory/              # 检查点 + 状态（aiosqlite WAL，崩溃可恢复）
│     ├─ observability/       # trace / metrics / 结构化日志（脱敏）
│     └─ events.py            # 事件总线 → SSE
├─ apps/
│  ├─ server/                 # FastAPI：REST + SSE + 审批端点
│  └─ web/                    # React + Vite 仪表盘
├─ packages/
│  └─ shared/                 # 由 pydantic 生成的 TS 类型
├─ examples/
│  └─ run.py                  # 端到端最小闭环（CLI 审批）
└─ tests/                     # 单测（每个 agent/规则可独立测）
```

## 5. 核心闭环（一次交易的生命周期）

```
① 采集   orchestrator 拉取 symbol 的 MarketSnapshot（行情+衍生品+链上+情绪）
② 分析   4 位分析师并行产出 AnalystReport（含降级：数据缺→跳过该维度）
③ 决策   首席策略官合成 TradeProposal（方向/标的/仓位/入场/止损/止盈/置信度/理由）
④ 风控   风控引擎硬校验 → 通过则风控官 LLM 软评审 → RiskAssessment（可否决/降档）
         └─ 否决 → 反馈注入策略官重议（≤ maxRetries 次）
⑤ 审批   提案+风控结论推送仪表盘/CLI，人工 批准/拒绝/修改 → ApprovalDecision
⑥ 执行   交易员将 OrderIntent 交给 Venue，幂等下单，跟踪成交 → ExecutionResult
⑦ 复盘   持仓平仓后，复盘官归因（对/错/滑点/时机）→ TradeReview，经验回灌策略库
```

每一步都发事件到总线 → SSE 实时推仪表盘；每一步落检查点，崩溃后 `resume(run_id)` 断点续跑。

## 6. MVP（M0）定义 —— 「骨架闭环」

> M0 的唯一目标：**在 PaperVenue 上端到端跑通上面 7 步，零真实资金、零外部密钥也能演示。**

M0 必须包含：
- [x] Venue 抽象 + `PaperVenue`（模拟撮合）+ `CexVenue` 只读行情（Binance 现货）
- [x] 数据管线：行情/K线/资金费（真实只读）+ 情绪（mock）
- [x] 分析师团：技术面（真实指标）、衍生品（真实资金费/OI）、情绪（mock 降级）
- [x] 首席策略官（LLM，缺 Key 时降级为规则模板）
- [x] **确定性风控引擎**（仓位/单笔风险/止损必填/日回撤熔断）+ 风控官（LLM 软评审）
- [x] 审批门（CLI 交互 + 仪表盘按钮二选一）
- [x] 交易员 → PaperVenue 模拟成交 + 复盘官
- [x] **运行时最小版**（见 [RUNTIME.md](RUNTIME.md)）：入场即挂（模拟）保护单、启动时对账、扫描/监控双循环最小实现
- [x] 仪表盘最小版：信号流 + 待审批卡片 + 持仓/PnL
- [x] 可观测：trace_id、结构化日志、`metrics.snapshot()`

M0 **不含**：真实下单、合约实盘、链上执行、回测、多所。（这些在 M1+，见 ROADMAP.md）

## 7. 里程碑总览

| 里程碑 | 主题 | 一句话验收标准 |
| --- | --- | --- |
| **M0** | 骨架闭环 | 模拟盘端到端跑通 7 步，无密钥可演示 |
| **M1** | 合约永续 | 资金费/持仓/杠杆/爆仓价纳入分析与风控，模拟盘跑合约 |
| **M2** | 实盘接入（CEX） | 最小权限 Key 真实下单，Kill Switch + 实时风控看板可用 |
| **M3** | 链上 DeFi | 钱包/DEX swap（隔离签名器 + 授权护栏 + MEV 防护），模拟→小额实盘 |
| **M4** | 多所 + 组合 | 跨所行情、组合级风控（总敞口/相关性/集中度） |
| **M5** | 策略库 + 回测 | 历史数据管线 + 回测引擎，策略参数化与择优 |
| **M6** | 进阶自动化 | 策略化自动审批（护栏内）、经验记忆增强、告警与巡检 |

详细功能拆解与验收见 [ROADMAP.md](ROADMAP.md)。

## 8. 环境搭建

```bash
# Python 3.11+
python -m venv .venv && source .venv/Scripts/activate   # Windows Git Bash
pip install -e ".[dev]"                                   # 核心 + 测试
pip install -e ".[anthropic]"                             # 需要 LLM 时
pip install -e ".[onchain]"                               # 需要链上时（web3/eth-account）

cp .env.example .env        # 默认 paper 模式 + mock LLM，零密钥可跑
python -m cyp.examples.run --symbol BTC/USDT --mode paper
```

配置项全部在 `.env.example` 有注释说明。关键开关：
- `CYP_MODE=paper|live` —— 实盘需显式改 `live` 且满足前置校验
- `ANTHROPIC_API_KEY` —— 缺失则 LLM 降级为规则模板
- `CYP_APPROVAL=cli|dashboard|auto` —— `auto` 仅 M6 且受策略护栏约束

## 9. 协作与工程约定

> 目标：让「多人/多智能体协作开发」这件事本身也是低摩擦、可并行、可回滚的。

- **契约先行**：动 `contracts/models.py` 需在 PR 描述里说明，因为它是前后端唯一真相；改完运行 `scripts/gen-types` 同步 TS。
- **每个 Agent 是纯模块**：显式注入依赖（LLM 客户端、Venue、事件总线、存储），禁止在 Agent 内部 `import` 全局单例——便于单测与替换（对标两参照项目）。
- **风控规则必须有单测**：`risk/rules.py` 每条规则一个测试用例（含边界与否决路径）。这是与钱相关的代码，未测不合并。
- **降级路径必须存在且被测**：任何外部依赖（LLM/交易所/RPC/数据源）失效时，系统降级而非崩溃；PaperVenue + mock LLM 的端到端测试是 CI 门禁。
- **可观测强制**：每次 LLM/下单调用一个 span；日志 JSON 结构化且自动脱敏 key/私钥/token。
- **提交前**：`ruff check` + `pytest` + `npm run typecheck`（web）。
- **分支**：`feat/*` `fix/*` `docs/*`；PR 必须过 CI（含无密钥端到端测试）。

## 10. Definition of Done（每个特性的完工标准）

一个特性「完成」当且仅当：

1. ✅ 契约在 `contracts/` 定义，TS 类型已同步
2. ✅ 核心逻辑有单测；涉及风控的有否决/边界用例
3. ✅ 有降级路径且被测（无密钥/依赖失效不崩）
4. ✅ 关键动作有 trace/span/日志，且脱敏
5. ✅ 仪表盘能观测到该特性的状态（若面向用户）
6. ✅ 文档更新（本 docs/ 对应章节 + CHANGELOG）
7. ✅ 实盘相关特性额外：在 paper 模式验证 ≥ 1 个完整闭环，且 Kill Switch 生效

## 11. 首日任务清单（M0 施工顺序建议）

1. 搭骨架：`pyproject.toml`、目录、`config.py`、`contracts/models.py`（先定契约）
2. `venue/base.py` + `PaperVenue`（先让"执行"可跑）+ `CexVenue` 只读行情
3. `data/` 行情与指标管线 + `MarketSnapshot` 组装
4. `risk/engine.py` + `rules.py`（先把护栏立起来，配单测）
5. `agents/`：technical → derivatives → strategist → risk_officer（LLM 全部带 mock 降级）
6. `orchestrator.py` 串起 7 步 + `events.py` + `memory/` 检查点
7. `approval/gate.py`（先 CLI）+ `examples/run.py` 端到端
8. `apps/server`（SSE）+ `apps/web` 最小仪表盘
9. 复盘官 + metrics + 文档/CHANGELOG 收口

> 记住施工总原则：**先立护栏，再放智能；先跑通模拟，再碰真钱。**
