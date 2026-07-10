# cyp-agent Go 后端重构方案

> 文档状态：方案设计稿  
> 适用范围：将当前 Python 后端逐步重构为 Go，同时保持现有 React 仪表盘和交易行为兼容  
> 目标：可回滚、可验证、可灰度，优先保证资金安全和行为一致性

## 1. 摘要

当前项目不是单纯的 FastAPI CRUD 服务，而是一个包含行情采集、量化指标、多智能体编排、确定性风控、人工审批、CEX/链上执行、回测、对账和 SSE 事件流的交易系统。

重构建议采用“Go 模块化单体 + Python Agent 过渡服务”的路线：

1. 对外继续保持现有 `/api/*` REST 和 SSE 契约，前端基本不改。
2. 先建立 Go 服务骨架、契约测试和迁移桥接层，Python 仍作为行为基线。
3. 优先迁移确定性和基础设施模块：契约、配置、事件、风控、组合、模拟盘、持久化。
4. 再迁移行情和交易所适配器；实盘执行必须经过双跑、测试网和灰度验证。
5. 多智能体和回测暂时保留 Python，等 Go 边界稳定后再决定是否迁移。
6. 最终目标是 Go 负责 API、运行时、风控、持久化和交易执行；Python 可作为可选的 Agent/策略服务。

不建议直接停止 Python、一次性重写全部模块。那样会同时改变语言、并发模型、数值模型、交易所适配和状态恢复机制，难以区分问题来源，也不适合真实资金场景。

## 2. 当前系统基线

### 2.1 代码结构

| 当前模块 | 主要职责 | Go 重构关注点 |
| --- | --- | --- |
| `apps/server/main.py` | FastAPI、REST、SSE、后台任务、生命周期管理 | API 兼容、请求取消、SSE 断线重连、任务生命周期 |
| `core/cyp/contracts` | Pydantic 领域模型和跨层契约 | JSON 字符串金额、枚举、校验、时间格式 |
| `core/cyp/orchestrator.py` | 7 步交易闭环编排 | 状态机、幂等、审批等待、失败恢复 |
| `core/cyp/agents` | 技术/衍生品/情绪/链上分析、策略、风控官、交易员、复盘 | Python 过渡服务、LLM 结构化输出 |
| `core/cyp/risk` | 确定性硬风控 | 必须逐条保持结果一致，禁止浮点误差 |
| `core/cyp/venue` | Paper、CEX、Onchain、交易所适配层 | 保护单、保证金、持仓模式、精度、限频 |
| `core/cyp/data` | K 线、盘口、衍生品、合成行情和指标 | 外部数据失败隔离、并发请求、缓存 |
| `core/cyp/runtime` | 扫描、持仓监控、对账、常驻循环 | 多任务停止、租约、重启恢复 |
| `core/cyp/approval` | 人工审批、自动审批、超时策略 | 分布式一致性、审批审计 |
| `core/cyp/memory` | PostgreSQL 检查点和经验条目 | 事务、兼容旧表、查询排序 |
| `core/cyp/backtest` | 回测、统计、优化、历史数据归档 | Decimal、可复现随机数、是否迁移的边界 |
| `core/cyp/llm` | Anthropic/DeepSeek/Mock、重试、熔断和成本护栏 | HTTP 客户端、JSON Schema、超时、成本统计 |
| `core/cyp/observability` | 日志、指标、Trace、脱敏 | OTel、Prometheus、结构化日志 |

当前核心代码约 5.5k 行，Python 文件约 78 个；Go 工具链已可用，但仓库还没有 `go.mod`。

### 2.2 对外接口基线

前端通过 [apps/web/src/shared/api/client.ts](../apps/web/src/shared/api/client.ts) 使用以下接口：

| 方法 | 路径 | 用途 | 是否必须兼容 |
| --- | --- | --- | --- |
| GET | `/api/health` | 服务健康和运行模式 | 是 |
| GET | `/api/venues` | 场所能力和配置 | 是 |
| GET/POST | `/api/settings` | 读取/更新运行配置 | 是，密钥行为需加强保护 |
| GET | `/api/market` | 多场所行情聚合 | 是 |
| GET | `/api/positions` | 当前持仓 | 是 |
| POST | `/api/positions/close` | 手动平仓 | 是 |
| GET | `/api/risk` | 风控看板 | 是 |
| GET | `/api/pending` | 待审批项 | 是 |
| GET | `/api/portfolio` | 组合敞口 | 是 |
| GET | `/api/metrics` | 运行和 LLM 指标 | 是 |
| POST | `/api/backtest` | 回测 | 是，初期可由 Python 代理处理 |
| POST | `/api/run` | 异步触发一轮交易闭环 | 是 |
| POST | `/api/approvals/{run_id}` | 审批/拒绝/修改 | 是 |
| GET/POST | `/api/killswitch` | Kill Switch | 是，最终需持久化 |
| GET | `/api/events` | SSE 事件流 | 是 |
| GET | `/` | 仪表盘入口 | 可兼容，建议交给前端静态服务器 |

### 2.3 关键不变量

以下行为在任何阶段都不能被破坏：

1. `mode=live` 未显式确认时不能执行真实交易。
2. Kill Switch 开启时不能开新仓。
3. 对账未完成时不能开新仓，只能允许减仓、平仓和补保护。
4. 实盘每个新仓必须有有效保护单；保护单失败必须立即平掉裸仓。
5. 风控硬规则优先于 LLM，LLM 不能绕过硬规则。
6. 相同 `client_id`/`run_id` 重试不能重复成交。
7. 金额、价格、数量和费率不能因为语言迁移产生浮点误差。
8. 密钥、私钥、Token 不得出现在日志、事件、API 返回和 LLM 上下文中。
9. 外部单个数据源、LLM 或分析师失败时，应按现有策略降级，而不是击穿整个运行时。

## 3. 重构目标与非目标

### 3.1 目标

- 用 Go 提供长期运行的 API、调度、事件、状态管理和交易运行时。
- 保持现有 REST/SSE 响应结构，前端无需同步大规模改造。
- 保持 Paper 模式无密钥可运行。
- 保持风控规则的判定结果和拒绝原因可追溯。
- 支持单实例稳定运行，再为多实例和高可用预留接口。
- 让交易执行具备明确的订单状态机、幂等键和恢复路径。
- 建立可重复的回放测试，能够对比 Python 和 Go 的输出。

### 3.2 非目标

首个 Go 版本不包含以下范围：

- 不重新设计 React 仪表盘视觉和交互。
- 不在重构期间增加新的交易策略。
- 不同时引入 Redis、Kafka、Kubernetes 等基础设施，除非性能或多实例需求已经证明必要。
- 不把 LLM 输出当作交易事实来源。
- 不在没有测试网、Paper 和灰度验证的情况下直接切实盘。
- 不为了“微服务化”而拆成多个独立部署单元；首版以模块化单体为主。

## 4. 目标架构

### 4.1 总体架构

```text
React/Vite Dashboard
        │ REST + SSE，兼容现有 /api/*
        ▼
┌─────────────────────────────────────────────┐
│ Go cyp-server                               │
│                                             │
│ API / Auth / SSE                            │
│ Runtime / Orchestrator                      │
│ Approval / Kill Switch / Idempotency        │
│ Risk / Portfolio / Contracts                │
│ Data / Venue / Execution                    │
│ Persistence / Events / Observability        │
└───────────────┬─────────────────────────────┘
                │ 迁移期间 HTTP/JSON 内部桥接
                ▼
      Python Agent Service（过渡阶段）
      分析师 / Strategist / RiskOfficer / Reviewer / Backtest

                │
      PostgreSQL + TimescaleDB
                │
      Binance / OKX / Onchain / LLM Provider
```

### 4.2 为什么先采用模块化单体

交易流程存在强顺序和强一致性：采集、分析、策略、风控、审批、执行、复盘之间共享 `run_id`、订单状态和风控快照。首版拆成多个网络服务会增加：

- 任务重复执行和消息投递语义；
- 审批等待状态的保存和恢复；
- 事件顺序保证；
- 交易幂等和故障排查成本。

因此先在一个 Go 进程内用清晰 package 边界隔离模块。只有在实际出现吞吐或部署隔离需求后，再拆 Agent、回测或行情服务。

### 4.3 建议目录

```text
cmd/
  cyp-server/
    main.go
  cyp-worker/
    main.go                 # 可选：将运行时任务独立部署

internal/
  api/
    http.go
    handlers_health.go
    handlers_market.go
    handlers_positions.go
    handlers_run.go
    handlers_approval.go
    handlers_settings.go
    sse.go
    middleware.go
  config/
  contracts/
    market.go
    trading.go
    risk.go
    events.go
    json.go
  orchestrator/
    service.go
    state_machine.go
    run_context.go
  runtime/
    engine.go
    scanner.go
    monitor.go
    reconciler.go
    lease.go
  risk/
    engine.go
    rules.go
    drawdown.go
    exposure.go
  portfolio/
  approval/
  data/
    source.go
    synthetic.go
    cex.go
    aggregator.go
  venue/
    venue.go
    paper.go
    cex.go
    binance.go
    okx.go
    onchain.go
    protective_orders.go
  execution/
  llm/
  backtest/                # 初期可只做 Python client
  persistence/
    postgres.go
    repositories/
  events/
  observability/

api/
  openapi.yaml
  jsonschema/

db/
  migrations/

services/
  agent-python/            # 迁移期间保留的 Python HTTP 服务

go.mod
go.sum
```

包之间只允许单向依赖：

```text
api → orchestrator/runtime → risk/portfolio/venue/data
                                  ↓
                         persistence/events/observability
llm/agent-python → orchestrator
contracts ← 所有业务包依赖
```

禁止 `api` 直接调用交易所、禁止 `risk` 依赖 LLM、禁止 `venue` 直接修改 API 层状态。

## 5. 技术选型

| 领域 | 建议 | 选择理由 |
| --- | --- | --- |
| HTTP | Go `net/http` + `chi` | 轻量、可控，足以覆盖当前 REST/SSE |
| JSON | `encoding/json`，金额自定义字符串解码 | 保持前端当前 Numeric 兼容性 |
| Decimal | `shopspring/decimal` 或等价定点库 | 对应 Python `Decimal`，禁止 `float64` 参与资金计算 |
| PostgreSQL | `pgx/v5` + `sqlc` | 连接池、事务和类型安全，兼容 TimescaleDB |
| 配置 | 环境变量 + 显式配置结构 | 兼容 `CYP_` 前缀和 `.env` 语义 |
| 并发 | `context`、`errgroup`、channel、worker pool | 替代 `asyncio.gather/create_task` |
| 日志 | `log/slog` JSON | 标准库、结构化、易接现有日志系统 |
| 指标 | Prometheus client | 保持现有 metrics snapshot，同时提供 Prometheus 指标 |
| Trace | OpenTelemetry | 覆盖 run、LLM、行情和订单生命周期 |
| UUID | `google/uuid` | 与当前 run_id/client_id 语义兼容 |
| 重试 | 带 context 的指数退避 | 仅重试瞬态错误，不能重试未知订单状态 |
| 测试 | Go testing + testify/require | 单测、表驱动测试和集成测试 |
| Python 桥接 | 首期 HTTP/JSON | 改造成本低、可观察、便于逐步删除 |

交易所 SDK 不直接作为领域模型使用。无论使用 REST/WS 客户端还是第三方库，都必须包在 `venue` 适配层内，由适配层转换为内部 `Venue` 契约。

## 6. 领域契约设计

### 6.1 契约单一来源

迁移期间保留三层契约：

1. `api/openapi.yaml`：对外 REST 契约。
2. `api/jsonschema/*.json`：事件和领域对象 JSON Schema。
3. `internal/contracts/*.go`：Go 类型和自定义校验。

Python 的 Pydantic 模型仍作为第一阶段行为基线。每次契约变更必须同时更新：

- Python Pydantic 模型；
- OpenAPI/JSON Schema；
- Go 类型；
- TypeScript 类型或生成脚本；
- JSON fixture 和契约测试。

### 6.2 数值约定

对外 JSON 继续将金额、价格、数量、费率、PnL 等字段编码为字符串，例如：

```json
{
  "size_quote": "1000.00",
  "entry_price": "60000.12",
  "slippage_bps": "5.0000"
}
```

约束：

- Go 内部统一 `Decimal` 类型；
- 禁止先解析为 `float64` 再转 Decimal；
- PostgreSQL 使用 `NUMERIC`；
- 显式定义量化和舍入规则，重点覆盖 `ROUND_DOWN`；
- 前端继续接受 `string | number`，但新接口统一输出 string；
- 测试必须覆盖极小数、手续费、杠杆、爆仓价和临界值。

### 6.3 核心对象

需要首先实现并冻结以下对象：

- `Candle`
- `OrderBook`
- `DerivativesData`
- `MarketSnapshot`
- `AnalystReport`
- `TradeProposal`
- `RiskAssessment`
- `ApprovalDecision`
- `OrderIntent`
- `ExecutionResult`
- `Position`
- `Balances`
- `TradeReview`
- `DashboardEvent`

### 6.4 事件契约

至少保持以下事件名称和主要字段：

```text
run_started
snapshot_ready
reports_ready
proposal_ready
risk_assessed
approval_decided
executed
reviewed
run_done
run_failed
killswitch
position_monitor
```

事件必须包含：

```json
{
  "type": "run_done",
  "run_id": "abc123",
  "ts": "2026-07-10T10:00:00Z",
  "symbol": "BTC/USDT",
  "status": "executed"
}
```

事件发布采用“先持久化、再广播”原则。SSE 客户端断线后，Go 服务应支持按 `Last-Event-ID` 或 `run_id` 查询补发；若首期不实现事件回放，也必须明确返回“只提供实时事件”的能力标识。

## 7. 模块迁移映射

| 阶段 | Python 模块 | Go 目标 | 迁移策略 |
| --- | --- | --- | --- |
| 1 | `contracts`, `config` | `internal/contracts`, `internal/config` | 先生成 JSON fixture，双向校验 |
| 1 | `events`, `observability` | `internal/events`, `internal/observability` | 先兼容日志和事件格式 |
| 2 | `memory` | `internal/persistence` | 兼容现有 `lessons/checkpoints` 表 |
| 2 | `risk`, `portfolio` | `internal/risk`, `internal/portfolio` | 最先做 Go 影子计算，禁止直接切交易 |
| 2 | `approval` | `internal/approval` | 先做内存单实例，再做数据库持久化 |
| 2 | `venue/paper` | `internal/venue/paper.go` | 作为 Go 端到端验收底座 |
| 3 | `data`, `venue/aggregator` | `internal/data` | 外部调用失败隔离，保持降级行为 |
| 3 | `venue/cex`, `adapters` | `internal/venue/binance.go`, `okx.go` | 先只读，后测试网，再小额灰度 |
| 3 | `runtime/reconcile` | `internal/runtime/reconciler.go` | 先实现冻结新仓和远端为真 |
| 4 | `orchestrator` | `internal/orchestrator` | 先调用 Python Agent，再迁确定性步骤 |
| 4 | `llm` | `internal/llm` | 直接 HTTP 客户端，保留重试/熔断/成本上限 |
| 5 | `agents` | Python 服务或 Go Agent 包 | 按 Agent 独立迁移，不能和执行一起切换 |
| 5 | `backtest` | Python 保留或 `internal/backtest` | 先通过结果 fixture 验证，最后迁移 |
| 6 | `apps/server/main.py` | `internal/api`、`cmd/cyp-server` | Go 全量接管外部接口 |

## 8. Python 过渡服务

### 8.1 过渡期职责

Python 服务暂时负责：

- 分析师并行调用；
- Strategist 生成 `TradeProposal`；
- RiskOfficer 的 LLM 软评审；
- Reviewer 生成复盘；
- 回测接口；
- 必要时作为旧交易所适配器的临时代理。

Go 负责：

- 所有外部 API；
- run 生命周期和幂等；
- 确定性风险硬规则；
- 审批队列；
- 订单状态机；
- Kill Switch；
- 持久化、对账和最终执行授权。

### 8.2 内部协议

首期使用 HTTP/JSON，原因是可以直接复用现有 Pydantic 模型并方便抓包回放。建议接口：

```text
POST /internal/agent/analyze
POST /internal/agent/strategize
POST /internal/agent/review
POST /internal/backtest/run
GET  /internal/health
```

所有内部请求必须包含：

- `request_id`；
- `run_id`；
- `trace_id`；
- `deadline`；
- `schema_version`。

Python 服务不得直接下真实订单。真实订单只能由 Go `execution` 在完成硬风控、审批、对账状态和保护单校验后发出。

## 9. 交易执行和状态机

### 9.1 Run 状态机

```text
QUEUED
  → COLLECTING
  → ANALYZING
  → PROPOSAL_READY
  → RISK_ASSESSED
  → WAITING_APPROVAL
  → APPROVED
  → EXECUTING
  → EXECUTED / EXECUTION_FAILED
  → REVIEWED
  → DONE

任意阶段 → FAILED
对账中 → RECONCILING（禁止新开仓）
Kill Switch → BLOCKED（允许平仓/减仓）
```

状态转换必须由单一服务负责，不允许 API handler 直接修改订单或 run 状态。

### 9.2 订单状态机

```text
NEW
 → PREFLIGHT
 → SUBMITTING
 → ACKNOWLEDGED
 → PARTIALLY_FILLED
 → FILLED
 → PROTECTIVE_PLACED
 → CLOSED

异常分支：REJECTED / CANCELED / UNKNOWN / PROTECTIVE_FAILED / FLATTENING
```

关键规则：

- 下单前保存 `OrderIntent`；
- 交易所返回未知时不能盲目重试，必须查询订单；
- `client_id` 唯一索引；
- 成交确认后必须验证保护单状态；
- 保护单失败进入 `FLATTENING`，直到仓位为零或进入人工紧急处理；
- 平仓和 Kill Switch 路径优先级高于开仓。

### 9.3 幂等与并发

- 单个 `run_id` 使用数据库租约或 PostgreSQL advisory lock；
- 单个 symbol 的开仓、平仓、对账串行化；
- API 可重复提交时返回已有任务结果；
- worker 使用 `context.WithCancel`，服务停止时先停止开仓，再等待减仓/查询任务；
- 任何后台 goroutine 必须有 owner、取消路径和错误上报；
- 所有共享 map 必须封装在对象内部并配锁或改为 actor/channel 模型。

## 10. Venue 和交易所迁移策略

### 10.1 Go 内部接口

```go
type Venue interface {
    ID() string
    Kind() VenueKind
    Caps() VenueCaps
    IsConfigured() bool

    FetchTicker(ctx context.Context, symbol string) (decimal.Decimal, error)
    FetchOHLCV(ctx context.Context, symbol, timeframe string, limit int) ([]Candle, error)
    FetchOrderBook(ctx context.Context, symbol string, depth int) (OrderBook, error)

    Positions(ctx context.Context) ([]Position, error)
    Balances(ctx context.Context) (Balances, error)
    Preflight(ctx context.Context, intent OrderIntent) (PreflightReport, error)
    Place(ctx context.Context, intent OrderIntent) (ExecutionResult, error)
    Cancel(ctx context.Context, orderID string) error
}
```

接口只表达上层需要的语义。Binance/OKX 特有的参数、持仓模式和保护单映射放在适配器内部。

### 10.2 迁移顺序

1. `PaperVenue`：确保 Go 端到端逻辑和保护单测试完整。
2. CEX 公共行情：ticker、OHLCV、盘口、资金费、OI。
3. CEX 只读账户：balances、positions、open orders、fills。
4. Testnet/Demo：下单、撤单、保护单、平仓、异常恢复。
5. 小额 live canary：只允许白名单 symbol 和极小额度。
6. 多场所聚合和跨场所组合风控。

### 10.3 交易所验收项

每个交易所都必须有独立适配测试：

- 现货/永续 symbol 映射；
- 精度和最小下单量；
- 单向/双向持仓模式；
- isolated/cross 保证金模式；
- reduce-only；
- stop-loss/take-profit 原生保护单；
- 部分成交；
- 网络超时后订单状态未知；
- 撤保护单和补保护单；
- 交易所限频和重试；
- demo/testnet 与真实环境的配置隔离。

## 11. 数据库和持久化方案

### 11.1 兼容现有表

现有表：

- `lessons`
- `checkpoints`

第一阶段 Go 直接兼容现有表，不修改字段含义。`checkpoints.data` 继续保存 JSON，便于 Python/Go 共同读取。

### 11.2 建议新增表

```text
runs
run_steps
approvals
orders
order_events
protective_orders
positions_snapshots
reconciliations
runtime_controls
audit_events
```

建议字段原则：

- 金额使用 `NUMERIC`；
- 时间使用 `TIMESTAMPTZ`；
- 交易所原始响应保存在脱敏后的 `JSONB`；
- `run_id`、`client_id`、`order_id` 和 `tx_hash` 建索引；
- 订单和事件采用 append-only，状态通过最新快照或投影读取；
- 密钥、私钥和完整签名材料不入库。

### 11.3 一致性

- 下单前状态、幂等记录和审计事件在一个事务中提交；
- 交易所调用不放在数据库事务中长时间占用连接；
- 交易所返回后使用单独事务写入结果；
- 每次服务启动先执行 reconcile；
- reconcile 未完成时 `runtime_controls` 标记为 `RECONCILING`；
- Kill Switch 以数据库为真，进程内只做短期缓存。

首期单实例可用 PostgreSQL 本地事件表加内存 channel。未来多实例时使用 PostgreSQL `LISTEN/NOTIFY` 通知，再从事件表读取完整事件；不把 NOTIFY payload 当作唯一事件存储。

## 12. API 兼容方案

### 12.1 兼容原则

- 路径、方法、状态码和主要字段保持不变；
- 错误体继续使用 `{"detail":"..."}`，逐步增加标准 `code`；
- Decimal 默认输出字符串；
- 时间统一 RFC3339 UTC；
- 未配置行情时保持“空数据但不阻断”的降级行为；
- `/api/run` 继续快速返回 `run_id`，实际流程异步执行；
- `/api/events` 继续使用 `text/event-stream`、keepalive 和断线清理；
- `/api/settings` 永远不返回原始 API key。

### 12.2 建议新增但不破坏兼容的能力

```text
GET /api/runs/{run_id}
GET /api/runs/{run_id}/events
GET /api/orders/{client_id}
GET /api/reconciliation
GET /api/ready
```

新增接口用于恢复和运维，不要求前端首版使用。

### 12.3 配置兼容

继续支持当前环境变量：

```text
CYP_MODE
CYP_APPROVAL
CYP_EXECUTION_VENUE
CYP_DATA_SOURCE
CYP_CEX_ID
CYP_DB_URL
CYP_WATCHLIST
CYP_RUNTIME_AUTOSTART
CYP_SCAN_INTERVAL
CYP_MONITOR_INTERVAL
CYP_KILL
ANTHROPIC_API_KEY
DEEPSEEK_API_KEY
BINANCE_API_KEY
BINANCE_API_SECRET
OKX_API_KEY
OKX_API_SECRET
OKX_PASSWORD
```

Go 端对布尔值、Decimal、列表和枚举的解析必须写表驱动测试，避免与 Pydantic 的隐式解析行为产生差异。

## 13. 分阶段实施计划

### Phase 0：基线冻结和安全护栏

目标：在写 Go 代码前，建立可以判断“行为是否改变”的基线。

工作项：

- 修复本地 Python 测试环境，确保现有测试可重复运行；
- 记录当前 API OpenAPI、事件样例和关键 JSON fixture；
- 给风控、Paper、审批、保护单、对账建立独立回放样例；
- 记录当前工作区未提交改动，迁移分支不要混入无关改动；
- 明确 Paper、Binance testnet、OKX demo 的凭据和账户隔离；
- 为真实交易增加 API 身份认证和操作员审计要求。

退出条件：

- 现有测试全部可运行；
- 关键 API 有 fixture；
- 至少有一组成功交易、一组拒绝交易、一组保护单失败样例；
- Kill Switch、对账冻结和幂等规则都有自动化断言。

### Phase 1：Go 工程骨架和契约层

工作项：

- 初始化 `go.mod`、目录结构和 CI；
- 实现 Decimal、时间、枚举和 JSON 字符串解码；
- 将 Pydantic 模型导出为 JSON Schema；
- 创建 Go contracts 和契约测试；
- 实现配置读取和脱敏快照；
- 实现结构化日志、request_id、trace_id 和基础指标；
- 建立 Python fixture 与 Go 反序列化的 golden tests。

退出条件：

- 所有核心对象可在 Python 和 Go 间往返序列化；
- 金额、时间、枚举和空值行为一致；
- `go test ./...` 和现有 Python 测试都可在 CI 执行。

### Phase 2：Go API 壳和 Python 代理

工作项：

- 实现所有现有 REST handler；
- 实现 SSE 实时广播、keepalive、客户端清理；
- Go API 通过 HTTP/JSON 调用 Python 过渡服务；
- 增加 `CYP_BACKEND_IMPL=python|go|shadow`；
- 保持 `/api/run`、审批和事件链路不变；
- 以现有前端做端到端回归。

退出条件：

- 前端无需修改即可连接 Go API；
- API fixture 对比通过；
- 运行时错误、超时和 Python 服务不可用时有明确降级；
- Go 服务关闭时不会遗留不可回收的后台任务。

### Phase 3：迁移确定性核心

迁移顺序：

1. PostgreSQL repository 和 checkpoint；
2. EventBus 和 metrics；
3. PortfolioTracker/PortfolioView；
4. Risk engine；
5. Approval gate；
6. PaperVenue；
7. Runtime reconcile/monitor/scanner。

工作方式：Go 影子计算 Python 结果，不执行真实订单。每个 risk case 记录：

- 输入快照；
- Python 结果；
- Go 结果；
- hard violations；
- adjusted size；
- 差异原因。

退出条件：

- 风控回放 100% 一致，或每个差异都有明确批准；
- Paper 端到端闭环通过；
- 对账失败、保护单失败、Kill Switch 和审批超时都有测试；
- Go 可独立跑通无密钥 Demo。

### Phase 4：行情和 CEX 适配

工作项：

- 先迁移公共行情；
- 再迁移账户只读接口；
- 加入交易所精度、限频和错误分类；
- 实现 Binance/OKX 独立适配器；
- 运行 Go/Python 行情双读比对；
- 运行交易所 testnet/demo 测试；
- 对未知订单状态采用查询确认，禁止简单重试。

退出条件：

- 只读行情和账户数据差异在允许阈值内；
- 两个交易所的保护单和撤单测试通过；
- 网络故障、限频、部分成交和重启恢复测试通过；
- 真实环境只允许白名单和最小额度。

### Phase 5：Go Orchestrator 和 Runtime 接管

工作项：

- Go 编排器接管采集、风险、审批和执行顺序；
- Agent 通过内部协议返回分析和策略结果；
- Go 持久化 run step 和事件；
- 实现启动对账和任务租约；
- 以 shadow 模式执行 Go/Python run，不重复下单；
- 通过 per-symbol lock 防止扫描和手工操作冲突；
- 逐个 endpoint 和逐个 symbol 灰度切换。

退出条件：

- Go 能完成 Paper 全闭环；
- Go 进程重启后能恢复待审批、待查询和对账状态；
- 事件顺序、run status、metrics 与 Python 基线一致；
- 新仓保护单覆盖率为 100%。

### Phase 6：实盘灰度和旧服务下线

灰度顺序：

1. Paper；
2. 交易所 Demo/Testnet；
3. 只读 live 账户；
4. 单一 symbol、极小额度；
5. 单一交易所全量；
6. 多 symbol、多交易所；
7. 关闭 Python 交易执行能力；
8. Python 仅保留 Agent/回测，或完全下线。

每一步都需要保留旧 Python 版本作为回滚目标，并且回滚前先停止新开仓、完成远端对账。

## 14. 测试方案

### 14.1 单元测试

- Decimal 运算、量化和舍入；
- 风控每条规则的边界；
- 组合敞口和相关性聚合；
- 订单状态转换；
- 保护单触发和裸仓平仓；
- 配置解析和脱敏；
- 事件序列化；
- SSE keepalive 和断线清理；
- 重试分类和 context 取消。

### 14.2 契约测试

- Go response 与现有 TypeScript 类型兼容；
- Python/Go 请求响应互相解析；
- 所有 Decimal 字段保持字符串；
- 404、422、502 等状态码兼容；
- 事件字段和事件顺序兼容；
- Settings 返回不包含任何密钥内容。

### 14.3 回放和差分测试

为以下场景保存 JSON fixture：

- 上涨行情开多；
- 下跌行情开空或拒绝；
- 无止损 proposal；
- 超过最大仓位；
- 超过总敞口；
- Kill Switch；
- 回撤熔断；
- 连续亏损熔断；
- 永续杠杆和维持保证金率；
- 对账发现外部持仓；
- 保护单下单失败；
- LLM 超时并降级到规则模板；
- 审批超时、拒绝和修改。

### 14.4 集成和故障测试

- PostgreSQL 不可用；
- Python Agent 不可用；
- 行情源部分失败；
- LLM 429/5xx/超时；
- CEX 网络中断；
- 下单响应丢失；
- 服务在提交订单前后重启；
- SSE 客户端大量断线重连；
- 多个扫描任务同时触发同一 symbol。

### 14.5 实盘前强制门禁

以下任一项不满足，禁止切实盘：

- `go test ./...` 全部通过；
- Python 基线测试通过；
- 契约和差分测试无未解释差异；
- Paper 运行至少完成一轮完整闭环；
- testnet/demo 完成开仓、部分成交、保护单、平仓和重启恢复；
- 保护单失败自动平仓验证通过；
- Kill Switch 和对账冻结验证通过；
- API 认证、密钥脱敏、操作审计已启用；
- 有明确的值班、告警和回滚操作手册。

## 15. 可观测性和运维

### 15.1 日志字段

所有 run、订单和交易所调用至少包含：

```text
ts
level
service
trace_id
request_id
run_id
client_id
symbol
venue
stage
status
duration_ms
error_code
```

日志中禁止出现：

```text
api_key
api_secret
password
private_key
token
authorization
签名原文
```

### 15.2 核心指标

- `runs_total{status}`；
- `run_duration_seconds`；
- `approval_latency_seconds`；
- `orders_total{venue,status}`；
- `orders_unknown_total`；
- `protective_order_failures_total`；
- `flatten_attempts_total`；
- `reconciliation_failures_total`；
- `risk_rejections_total{reason}`；
- `llm_requests_total{provider,status}`；
- `llm_tokens_total`；
- `sse_clients`；
- `event_delivery_lag_seconds`。

### 15.3 健康检查

- `/api/health`：进程存活和基本配置；
- `/api/ready`：数据库、Agent、交易所连接和对账状态；
- 交易执行前必须检查 `ready` 和 `reconciling=false`；
- 健康检查不能因为单个行情源短暂失败而误报整个服务不可用。

## 16. 安全改造

当前 API 允许通过 `/api/settings` 更新 LLM 配置和密钥。Go 重构时应保留必要兼容性，但在生产模式增加：

- 操作员认证和角色权限；
- settings 更新审计；
- API Key 只允许写入 Secret Manager/KMS 或环境变量，不写数据库；
- 生产环境禁止通过普通仪表盘接口提交交易所私钥；
- 审批、Kill Switch、平仓和保护单补挂需要操作员审计；
- CEX Key 默认禁止提现权限；
- 链上私钥继续隔离在 signer/KMS/hardware 层，Go 业务包不得读取明文私钥；
- 日志、SSE、错误信息和 LLM prompt 做统一脱敏；
- 真实交易接口默认 deny，必须由 live guard 显式打开。

认证和权限属于生产门禁，不能因为“兼容现有前端”而省略。可以先实现内部操作员 token，再逐步接入 SSO/OIDC。

## 17. 部署方案

### 17.1 开发环境

```text
PostgreSQL/TimescaleDB：docker-compose
Python Agent：uvicorn 或现有启动脚本
Go API：go run ./cmd/cyp-server
React：npm run dev
```

### 17.2 生产初期

```text
go cyp-server
  ├── REST/SSE
  ├── Runtime worker
  ├── Risk/Approval/Execution
  └── Python Agent HTTP client

PostgreSQL/TimescaleDB
Reverse Proxy / TLS
Secret Manager
```

先单实例部署，避免在状态持久化和事件回放尚未成熟前引入多实例竞争。

### 17.3 未来扩展

当以下条件满足后再拆 worker：

- API 请求和运行时任务已有独立租约；
- 事件表和回放机制完成；
- 多实例下 Kill Switch 和审批状态可见；
- 订单幂等与对账不依赖进程内内存。

届时可拆为 `cyp-api` 和 `cyp-worker`，但仍共享 PostgreSQL 状态和同一 contracts 包。

## 18. 灰度、回滚和事故处理

### 18.1 切换开关

建议支持：

```text
CYP_BACKEND_IMPL=python|go|shadow
CYP_EXECUTION_IMPL=python|go
CYP_RISK_IMPL=python|go|dual
CYP_AGENT_IMPL=python
CYP_LIVE_ENABLED=false
CYP_CANARY_SYMBOLS=BTC/USDT
```

`shadow` 模式只计算和记录 Go 结果，不发送订单。

### 18.2 回滚步骤

1. 打开 Kill Switch，停止新开仓；
2. 查询交易所远端 positions/open orders/fills；
3. 执行 reconcile，以远端为真；
4. 确认保护单存在，必要时补挂或平掉裸仓；
5. 将流量切回 Python；
6. 保留 Go 产生的审计、事件和错误日志；
7. 分析完成后才重新开启灰度。

禁止直接杀进程或回滚二进制后忽略远端订单状态。

### 18.3 故障分级

| 等级 | 示例 | 自动动作 |
| --- | --- | --- |
| P0 | 裸仓、重复下单、远端状态不明 | Kill Switch、停止开仓、通知值班人 |
| P1 | 对账失败、保护单异常、订单未知 | 冻结开仓，仅允许修复/平仓 |
| P2 | 行情源异常、LLM 不可用 | 降级数据或规则 Agent，不自动开高风险仓 |
| P3 | 仪表盘/SSE 异常 | 保持运行时，告警并恢复 UI |

## 19. 交付物

每个阶段必须交付代码、测试和文档，不只交一个可启动的二进制。

### 必需文件

- `go.mod` / `go.sum`；
- `api/openapi.yaml`；
- `api/jsonschema/`；
- `db/migrations/`；
- Go package 文档；
- `docs/GO_OPERATIONS.md`；
- `docs/GO_ROLLBACK.md`；
- `docs/GO_EXCHANGE_TESTING.md`；
- CI 中的 `go test ./...`、静态检查和构建；
- Paper、Demo/Testnet 和回放 fixture；
- 灰度配置和回滚脚本。

### 交付验收表

| 类别 | 验收标准 |
| --- | --- |
| API | 前端不改代码即可访问 Go API |
| 契约 | JSON、Decimal、事件和错误结构兼容 |
| Paper | 无密钥可完成采集、分析、审批、执行、复盘 |
| 风控 | 所有硬规则结果与 Python 基线一致 |
| 审批 | approve/reject/modify/timeout 全覆盖 |
| 执行 | 幂等、保护单、部分成交、未知状态可恢复 |
| 对账 | 启动对账完成前禁止新开仓 |
| 安全 | 密钥和私钥不进入日志/API/LLM |
| 可观测 | run、order、reconcile 均有日志、指标和 trace |
| 灰度 | 支持 shadow、canary 和回滚 |
| 运维 | 有启动、停止、回滚和事故处理手册 |

## 20. 风险清单

| 风险 | 影响 | 缓解措施 |
| --- | --- | --- |
| Decimal 与舍入不一致 | 下单数量、风险预算错误 | 定点库、golden tests、禁止 float64 |
| Python/Go 规则漂移 | 风控结果不一致 | 双跑、fixture、差异审批 |
| 订单响应丢失 | 重复下单或状态错误 | UNKNOWN 状态、查询确认、幂等唯一键 |
| 保护单适配错误 | 裸仓风险 | 交易所独立测试网测试，失败自动平仓 |
| 进程内状态丢失 | 审批/run 丢失 | DB 持久化、启动恢复、租约 |
| SSE 事件丢失 | UI 状态过期 | 事件表、补发接口、定期轮询快照 |
| LLM 结构化输出不稳定 | 策略流程失败 | JSON Schema、重试、规则降级 |
| Python Agent 不可用 | 无分析结果 | mock/规则降级，健康检查和超时 |
| 多实例竞争 | 重复扫描和下单 | 单 symbol 锁、DB lease、先单实例 |
| 迁移与现有未提交改动冲突 | 基线不清 | 独立迁移分支，先固定当前测试快照 |

## 21. 推荐执行顺序

如果只能先做一部分，建议严格按以下顺序：

1. 补齐 Python 测试环境并固定基线；
2. 生成 OpenAPI、JSON Schema 和事件 fixture；
3. 建 Go contracts、Decimal、config 和测试框架；
4. 实现 Go PaperVenue、Risk 和 API shadow；
5. 用 Go 影子结果对比 Python；
6. 迁移 PostgreSQL、审批、对账和运行时；
7. 迁移 CEX 只读和 testnet/demo；
8. 迁移 Go Orchestrator，Python 仅提供 Agent；
9. 单 symbol 小额灰度；
10. 根据运行数据决定是否迁移 Agent 和 backtest；
11. 最后才下线 Python 服务或旧执行路径。

## 22. 最终判断

该项目改成 Go 在技术上可行，但合理的目标不是“把 Python 文件逐个翻译成 Go”，而是把系统拆成稳定的领域边界：

- Go 负责可靠运行时、状态机、风控、审批、持久化和交易执行；
- Python 在过渡期负责 Agent、LLM 和回测；
- 外部接口和事件契约保持不变；
- 通过 Paper、差分测试、testnet/demo 和 canary 逐步切换；
- 任何实盘切换都以对账、幂等和保护单验证为前置条件。

按照这个方案，项目可以先获得 Go 的服务治理和运行时能力，同时避免一次性重写造成交易行为不可解释、无法回滚或资金安全失控。
