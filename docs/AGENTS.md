# 多智能体规格

Go 实现把 Agent 限定为只读分析、提案、软评审和复盘模块。`internal/agents` 不依赖 `venue` 或 `approval`，因此任何 Agent 都拿不到下单能力；预检、硬风控、审批和执行由 `internal/orchestrator` 明确编排。

## 总体流程

```text
MarketSnapshot
   ├─ TechnicalAnalyst
   ├─ DerivativesAnalyst       四者并行、失败隔离、输出顺序确定
   ├─ SentimentAnalyst
   └─ OnchainAnalyst
              │
              ▼
          Strategist ── TradeProposal
              │
              ▼
      Venue.Preflight + risk.Assess（确定性）
              │
              ▼
        RiskOfficer（只能收紧）
              │
              ▼
       Approval + 再次硬风控
              │
              ▼
        PaperVenue.Place
              │
              ▼
           Reviewer ── lessons
```

当前没有可自主行动的“交易员 Agent”。订单意图由 Orchestrator 从已批准提案确定性构造，且 v0.2.0 只能交给 `PaperVenue`。

## 公共上下文

`internal/agents/base.go` 定义 Agent 可使用的最小依赖：

```go
type LLM interface {
	Enabled() bool
	Text(context.Context, string, string, bool) (string, error)
	JSON(context.Context, string, string, json.RawMessage, any, bool) error
}

type AgentContext struct {
	LLM       LLM
	AllowPerp bool
	Lessons   []string
}
```

每个 run 创建独立 LLM session，以便预算、熔断和指标不在并发 run 间串扰。Agent 不读取全局配置，不接触密钥；历史经验只以脱敏文本注入。

## 分析师接口

```go
type Analyst interface {
	ID() contracts.AgentID
	Run(
		context.Context,
		contracts.MarketSnapshot,
		AgentContext,
	) (contracts.AnalystReport, error)
}
```

`RunAnalysts` 并发执行固定 panel，同时按注册顺序写入结果。某个分析师报错时，该位置被替换为 `degraded=true` 的中性报告；父 context 取消才会终止整个 panel。

### TechnicalAnalyst

| 项 | 内容 |
| --- | --- |
| 输入 | `MarketSnapshot.OHLCV` |
| 规则 | SMA20/50、MACD/Signal、RSI、布林位置与 ATR 上下文 |
| 输出 | stance、confidence、signals、rationale |
| 降级 | 无有效 K 线时返回中性 degraded 报告 |

技术指标在 Go 中直接计算，不依赖外部数据框库。所有中间浮点结果必须拒绝 NaN/Inf，资金值仍使用 `contracts.Decimal`。

### DerivativesAnalyst

| 项 | 内容 |
| --- | --- |
| 输入 | funding rate、open interest、long/short ratio、basis 等衍生品字段 |
| 规则 | 资金费拥挤、OI 变化、多空失衡与基差投票 |
| 输出 | 统一 `AnalystReport` |
| 降级 | 衍生品字段缺失时中性 degraded，不阻断现货分析 |

### SentimentAnalyst

| 项 | 内容 |
| --- | --- |
| 输入 | `MarketSnapshot.Sentiment` 的分数、来源和摘要 |
| 规则 | 极端恐惧/贪婪与方向阈值 |
| 输出 | 统一 `AnalystReport` |
| 降级 | 无情绪源时权重自然归零 |

### OnchainAnalyst

| 项 | 内容 |
| --- | --- |
| 输入 | smart-money flow、流动性、集中度等链上摘要 |
| 规则 | 流向、池深和集中度风险投票 |
| 输出 | 统一 `AnalystReport` |
| 降级 | 无链上数据时返回中性 degraded |

链上分析师是只读分析模块，不持有 RPC 私钥或签名器。

## Strategist

`Strategist.Run` 输入报告、快照、权益、风险配置、AgentContext、场所 ID 和当前持仓，输出 `TradeProposal`。

规则路径：

1. 按分析师权重合成净方向和置信度。
2. 信号不足、参考价无效或已有同向仓位时返回 `flat`。
3. 根据 ATR 或 EWMA 波动设置止损/止盈距离。
4. 根据权益、单笔风险和止损距离反推 size，而非直接让模型决定金额。
5. 现货不允许做空；永续受 `AllowPerp`、逐仓和最大杠杆约束。
6. 若配置波动率目标，用 `target / EWMA` 进一步缩放，不得放大到风控上限之外。

默认权重：技术面 1.0、衍生品 0.9、情绪 0.6、链上 0.8；默认入场阈值 0.12、ATR 止损 2 倍、止盈 3 倍。这些是代码默认值，不是投资建议。

LLM 仅用于增强 thesis/结构化判断；输出无效、预算耗尽、请求失败或熔断时回到规则结果。LLM 无权跳过止损或直接生成订单。

## 确定性风控与 RiskOfficer

`risk.Assess` 先运行所有硬规则，产生 `approved`、`downsized` 或 `rejected`。`RiskOfficer` 只在硬结果未拒绝且 LLM 可用时运行：

- 可以提高 `risk_score`；
- 可以把结果升级为拒绝；
- 不能降低风险分；
- 不能复活硬拒绝；
- JSON/Provider 失败时保留原硬风控结论。

因此硬规则是最终下限，软评审永远只能收紧。

## 审批和执行边界

审批不属于 Agent：

- Dashboard 审批支持批准、拒绝和修改。
- `auto` 只有 symbol 白名单、风险分上限和金额上限同时满足才通过，否则仍进入 pending。
- 任何修改或硬风控缩量都会在执行前再次运行 `risk.Assess`。
- SafetyState、Kill Switch、mode 和 execution venue 在下单前再次检查。
- Orchestrator 从最终提案构造幂等 `OrderIntent`，交给 PaperVenue。

任何代码都不得把 LLM 输出直接传给 `Venue.Place`。

## Reviewer

`Reviewer.Run` 接收最终提案、执行结果和 run ID，输出 `TradeReview`：

- 执行失败时记录经过脱敏的原因和 preflight/场所检查建议；
- 滑点超过 20 bps 时提示限价或拆单；
- 低置信度成交提示缩仓/观望；
- lessons 按 symbol 有界持久化，供后续 Strategist/RiskOfficer 使用。

当前 Reviewer 评价的是执行结果，Paper 持仓开仓后不会在同一 run 中自动等待最终平仓 PnL，因此 `PNLQuote` 初始为 0。完整生命周期归因属于后续里程碑。

## LLM 安全与降级

LLM 客户端位于 `internal/llm`，支持 Anthropic 和 DeepSeek。关键约束：

- 缺少 provider key 时 `Enabled=false`，系统走规则路径。
- 每个 session 有迭代、token、成本和墙钟时间四重预算。
- 请求有超时、有限重试和熔断。
- 结构化输出必须通过 JSON Schema 与 Go 类型校验。
- prompt、输出和错误在进入日志/经验前做凭据、Bearer token、`sk-` key 和 PEM 私钥脱敏。
- LLM 故障不得改变硬风控或真实执行边界。

## 新增分析师

1. 在 `internal/agents` 实现 `Analyst`，保持无状态或把依赖显式注入。
2. 缺数据返回 degraded 报告；只有 context 取消或真正的内部错误才返回 error。
3. 在 `AllAnalysts()` 注册；注意顺序也是稳定 API 展示顺序。
4. 若新增契约字段，先改 `internal/contracts`、OpenAPI 和前端类型。
5. 添加规则、缺数据、错误隔离、取消、有限数值和敏感信息测试。

验证命令：

```bash
go test -race ./internal/agents ./internal/orchestrator ./internal/risk
```

## 不变量测试清单

- 单个分析师返回 error 时不泄漏敏感上游错误且不影响其他报告；实现不得 panic。
- 相同输入和无 LLM 时输出确定一致。
- `flat` 提案永不进入执行。
- 无止损、超风险、Kill 或对账冻结必被拒绝。
- RiskOfficer 无法降低硬风控风险分或复活拒绝。
- 审批修改后必须再次硬校验。
- 非 Paper 配置即使有密钥和确认变量，也无法打开真实下单。
