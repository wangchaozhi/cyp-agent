# 多智能体协作规格 · cyp-agent

> 本文定义每个智能体的**职责、输入/输出契约、降级行为、协作与反馈闭环**。
> 通用原则：每个 Agent 是**显式注入依赖的纯模块**（LLM 客户端、Venue、数据源、事件总线、记忆），不 import 全局单例——便于单测与替换。每个 Agent 都必须有 **LLM 缺席时的规则降级路径**。

## 协作全景

```
                         ┌─────────────── 分析师团（并行、失败隔离）────────────────┐
   MarketSnapshot ──────▶│ 技术面      衍生品        情绪         链上              │
                         │ technical   derivatives   sentiment    onchain           │
                         └──────┬──────────┬───────────┬────────────┬───────────────┘
                                └──────────┴─────┬─────┴────────────┘
                                     [AnalystReport × N]
                                                 ▼
                                    ┌──────────────────────┐   否决理由
                                    │ 首席策略官 strategist  │◀───────────────┐
                                    └──────────┬───────────┘                  │
                                       TradeProposal                          │ 反馈重议
                                                ▼                             │ (≤maxRetries)
                        ┌───────────────────────────────────────────┐         │
                        │ 风控引擎 risk.engine（确定性，一票否决）    │─reject──┘
                        │        ↓ pass                              │
                        │ 风控官 risk_officer（LLM 软评审）          │
                        └───────────────────┬───────────────────────┘
                                     RiskAssessment
                                                ▼
                                    ┌──────────────────────┐
                                    │ 人工审批门 approval    │  批准/拒绝/修改
                                    └──────────┬───────────┘
                                     ApprovalDecision
                                                ▼
                                    ┌──────────────────────┐
                                    │ 交易员 trader          │──▶ Venue.place（幂等）
                                    └──────────┬───────────┘
                                     ExecutionResult
                                                ▼
                                    ┌──────────────────────┐
                                    │ 复盘官 reviewer        │──▶ memory.lessons（经验回灌）
                                    └──────────────────────┘
```

## 分析师团（并行专家）

四位分析师并行运行，各自独立、失败隔离，产出统一的 `AnalystReport`。首席策略官按 `confidence × 维度权重` 加权综合。

### 技术面分析师 `technical`
| 项 | 内容 |
| --- | --- |
| 输入 | `MarketSnapshot.ohlcv / orderbook` |
| 做什么 | 计算指标（均线/MACD/RSI/布林/ATR/量能）、识别形态、给支撑阻力、判趋势与背离 |
| 输出 | `stance / confidence / signals[] / rationale` |
| LLM 降级 | 纯规则：多指标投票 + ATR 定波动率；无 LLM 也能出结构化信号 |

### 衍生品分析师 `derivatives`（合约核心）
| 项 | 内容 |
| --- | --- |
| 输入 | `MarketSnapshot.derivatives`（资金费率、持仓量 OI、多空比、基差、爆仓数据） |
| 做什么 | 判断资金费拥挤度、多空失衡、逼空/踩踏风险、基差套利线索；为杠杆决策提供风险上下文 |
| 输出 | 同上；额外 `funding_regime / oi_trend / liquidation_risk` |
| LLM 降级 | 规则阈值：资金费极值 + OI 背离 → 反转预警 |

### 情绪分析师 `sentiment`
| 项 | 内容 |
| --- | --- |
| 输入 | `MarketSnapshot.sentiment`（恐贪指数、新闻情绪、社媒热度） |
| 做什么 | 聚合情绪面，识别极端贪婪/恐惧、突发事件、叙事热度 |
| 输出 | 同上 |
| LLM 降级 | 无源时 `degraded=true`、权重降 0；有源无 LLM 时用词典/指数规则 |

### 链上分析师 `onchain`（DeFi 核心）
| 项 | 内容 |
| --- | --- |
| 输入 | `MarketSnapshot.onchain`（聪明钱/巨鲸流向、DEX 流动性/池深、持有分布、交易所净流、代币解锁） |
| 做什么 | 识别聪明钱建仓/派发、流动性变化、集中度风险、合约/项目风险信号 |
| 输出 | 同上；额外 `smart_money_flow / liquidity_health / concentration_risk` |
| LLM 降级 | 无 RPC/数据源时跳过；有数据无 LLM 时用阈值规则 |

## 首席策略官 `strategist`（决策合成）

| 项 | 内容 |
| --- | --- |
| 输入 | `[AnalystReport × N]` + `memory.lessons`（历史经验）+ 当前持仓/账户状态 |
| 做什么 | 综合多维报告 → 决定 方向/标的/工具(现货或永续)/仓位/杠杆/入场计划/**止损(必填)**/止盈/置信度，并写明 thesis 与依据的报告 |
| 输出 | `TradeProposal` |
| 关键约束 | **止损必填**（缺止损会被风控引擎直接否决）；仓位按「单笔风险 ≤ 账户 x%」反推，不是拍脑袋 |
| 反馈处理 | 收到风控否决理由时，在下一次合成中显式规避（如降杠杆、缩仓、换入场价） |
| LLM 降级 | 规则加权：维度投票定方向，`账户风险预算 / 止损距离` 定仓位，模板化 thesis |

## 风控层（两级：硬护栏 + 软评审）

### 风控引擎 `risk.engine` —— ★ 确定性，一票否决，非 LLM
这是整个系统的**命门**，独立于所有 LLM。是纯函数集合，逐条校验 `TradeProposal + preflight + 组合状态`：

- 单笔风险 R ≤ 账户 x%（默认 1%）；无止损 → **直接否决**
- 单仓上限 / 总敞口上限 / 单标的集中度上限
- 杠杆上限（按品种，如永续 ≤ 3x 起步）
- 日/周/总回撤熔断（触发即冻结新开仓，进冷静期）
- 连亏 N 次 → 强制冷静期
- 下单频率上限、滑点上限
- 链上：授权额度上限（禁无限授权）、合约白名单/蜜罐检查、最小流动性、gas/价格冲击上限
- Kill Switch 生效时 → 全部否决

输出 `verdict ∈ {approved, downsized, rejected}` + `hard_violations[]`。`downsized` 会给出 `adjusted_size_quote`。**只有 `approved/downsized` 才进入软评审与审批**。规则清单与阈值见 [RISK.md](RISK.md)，每条规则一个单测。

### 风控官 `risk_officer` —— LLM 软评审（护栏之上）
| 项 | 内容 |
| --- | --- |
| 输入 | 通过硬护栏的提案 + 全部分析报告 + 市场状态 |
| 做什么 | 做人类风控官会做的定性审查：thesis 是否自洽？是否在极端行情/重大事件窗口？相关持仓是否叠加同向风险？给 `risk_score` 与改进建议 |
| 输出 | 合并进 `RiskAssessment`（`llm_notes / risk_score`），可建议 `downsized` 或 `rejected` |
| 关键约束 | **软评审不能放宽硬护栏**，只能收紧或提示；无权批准被硬护栏否决的提案 |
| LLM 降级 | 跳过软评审，仅凭硬护栏结论放行到审批门（附「未软评审」标记） |

## 人工审批门 `approval.gate`（半自动的定义所在）

| 项 | 内容 |
| --- | --- |
| 输入 | `TradeProposal + RiskAssessment` |
| 做什么 | 推送到仪表盘/CLI，展示提案全貌 + 风控结论 + 依据报告；等待操作员 **批准 / 拒绝 / 修改** |
| 输出 | `ApprovalDecision`（含操作员、时间、备注，全审计落库） |
| 超时策略 | 默认超时 → **拒绝**（fail-safe，绝不默认放行）；超时时长可配 |
| 修改路径 | 操作员改仓位/入场/止损 → 修改后的提案**重新过硬护栏**再执行 |
| M6 演进 | `CYP_APPROVAL=auto` 时，满足「策略白名单 + 风控 risk_score 低 + 金额小」可自动批准，否则仍转人工 |

## 交易员 `trader`（执行编排）

| 项 | 内容 |
| --- | --- |
| 输入 | `ApprovalDecision(approve/modify)` |
| 做什么 | 生成带幂等 `client_id` 的 `OrderIntent` → `venue.preflight` 二次体检 → `venue.place` → 跟踪订单生命周期（部分成交/撤改/超时重试）→ 记录实际滑点 |
| 输出 | `ExecutionResult` |
| 幂等 | `client_id` 作为交易所 `clientOrderId` / 链上 nonce 去重键，崩溃重放不重复成交 |
| 降级 | `paper` 模式走 `PaperVenue` 模拟撮合；实盘失败按可重试性处理，不可重试则标记 FAILED 并告警 |
| 边界 | 交易员**不做决策**，只忠实执行已审批意图；执行前的最后 preflight 若发现市况剧变超阈值可中止并回报 |

## 复盘官 `reviewer`（经验闭环）

| 项 | 内容 |
| --- | --- |
| 输入 | 平仓后的 `ExecutionResult + 持仓历史 + 当时的报告/提案` |
| 做什么 | 归因：方向对错、入场/止损/止盈质量、滑点、时机、假设是否成立；产出结构化经验条目 |
| 输出 | `TradeReview`（评分 + `lessons[]`），写入 `memory/` |
| 反馈闭环 | `lessons` 在后续轮次注入分析师/策略官上下文，形成轻量长期记忆（对标 game-asset-forge 审查反馈回灌） |
| LLM 降级 | 仅做量化归因（PnL/滑点/持仓时长统计），跳过定性总结 |

## 协作契约与不变量（Invariants）

1. **护栏先于智能**：任何 `TradeProposal` 必须先过 `risk.engine` 硬护栏，LLM 软评审无权放宽。
2. **止损不可缺**：无 `stop_loss` 的提案在风控层必被否决。
3. **人工兜底**：`paper` 之外的每一笔真实下单必过审批门；审批超时=拒绝。
4. **失败隔离**：单个分析师失败不阻断闭环，只降级标记。
5. **降级恒存**：每个 Agent 都有无 LLM 的规则路径；系统无密钥可端到端跑通。
6. **幂等执行**：同一 `client_id` 永不重复成交。
7. **全程可观测**：每个 Agent 一个 span，输入输出脱敏落日志，异常必告警。

## Agent 接口约定（便于新增/替换）

```python
class Agent(Protocol):
    id: str
    async def run(self, ctx: RunContext, inp: BaseModel) -> BaseModel: ...
    # ctx 注入：llm, venue, data, events, memory, config
    # 实现内部禁止读全局单例；LLM 异常必须 try→规则降级
```

新增一个分析师：实现 `Agent` → 在 `orchestrator` 的分析师团注册 → 定义其 `AnalystReport` 子字段（若有）→ 补单测（含降级用例）→ 仪表盘信号流自动展示。
