# 架构设计

本文描述 `main` 当前实现。后端、运行时、智能体、风控、回测和命令行均为 Go；前端为 React/Vite。旧实现只存在于历史归档分支，不参与主分支构建或运行。

## 设计原则

- **安全优先**：确定性风控、审批、Kill Switch、启动对账和执行场所检查均在下单前生效。
- **默认可运行**：零密钥时使用合成行情、规则/Mock LLM 和 Paper 撮合，仍可跑通完整闭环。
- **实盘默认拒绝**：默认配置只允许 Paper 开仓；OKX Demo 与 OKX 实盘都必须显式配置，实盘还需静态安全门禁、动态账户校验、启动对账与 PostgreSQL 单实例租约全部通过；Binance 和链上真实下单保持代码级硬禁用。
- **契约明确**：Go DTO 位于 `internal/contracts`；外部 HTTP/SSE 契约分别位于 `api/openapi.yaml` 和 `api/jsonschema/`。
- **状态可恢复**：检查点和复盘经验可存入内存、原子 JSON 文件或 PostgreSQL。
- **边界可替换**：行情、LLM、Venue 和持久化均通过 Go 接口注入。

## 总览

```text
apps/web (React/Vite)
        │ REST + SSE
        ▼
cmd/cyp-server → internal/api (net/http)
        │
        ├─ control/config ── 运行配置、Kill Switch、脱敏快照
        ├─ orchestrator ──── 一轮交易工作流
        │    ├─ data ─────── 合成行情或 CEX 公共行情
        │    ├─ agents/llm ─ 分析、策略、软风控、复盘
        │    ├─ risk ─────── 确定性硬风控
        │    ├─ approval ─── 人工或受限自动审批
        │    └─ venue ────── Paper / OKX Demo / OKX 实盘执行
        ├─ runtime ───────── 启动对账、扫描、持仓监控
        ├─ persistence ───── memory / atomic JSON / PostgreSQL
		├─ ohlcv ─────────── 已闭合 K 线异步归档、保留与缺口补录
        └─ events/metrics ── SSE 事件、运行指标、JSON 日志

cmd/cyp ── backtest / sweep / config / flatten / version
```

## 启动与装配

`cmd/cyp-server` 是唯一服务入口：

1. `internal/config.Load` 读取默认值、`.env` 和进程环境变量；进程环境变量优先。
2. `internal/app.New` 创建控制状态、事件总线、PaperVenue、Binance/OKX 适配器、行情源、Repository、LLM、智能体、编排器和 RuntimeEngine，并只把 Paper、显式 OKX Demo 或通过生产门禁的 OKX 实盘选作执行场所；实盘还会先取得 PostgreSQL 租约并核验 OKX 账户就绪。
3. RuntimeEngine 必须先完成当前执行场所的启动对账。对账失败时应用构建失败，新开仓保持冻结。
4. `CYP_RUNTIME_AUTOSTART=true` 时，对账后再启动机会扫描和持仓监控；否则只完成一次启动对账。
5. API 使用 Go 标准库 `net/http` 提供 REST、SSE 和已构建前端静态文件。
6. 收到 `SIGINT`/`SIGTERM` 后停止运行时、审批等待与事件流，再执行 HTTP 优雅关闭。

## 包职责

| 目录 | 职责 |
| --- | --- |
| `cmd/cyp-server` | HTTP 服务入口、信号处理、结构化日志 |
| `cmd/cyp` | 回测、扫参、脱敏配置、应急清仓和版本命令 |
| `internal/app` | 依赖装配与资源生命周期 |
| `internal/contracts` | DTO、枚举、事件和精确 Decimal |
| `internal/config` | `.env`/环境变量解析、校验、密钥脱敏、实盘静态安全门禁与可信区域域名映射 |
| `internal/api` | REST、SSE、静态前端、请求限制和 request ID |
| `internal/orchestrator` | 异步 run 生命周期与检查点 |
| `internal/agents` | 并行分析师、策略官、风险官和复盘官 |
| `internal/llm` | Anthropic、DeepSeek、Mock、重试/熔断/预算 |
| `internal/risk` | 仓位、敞口、滑点、杠杆、回撤等确定性规则 |
| `internal/approval` | pending 队列、approve/reject/modify、超时 |
| `internal/venue` | 统一 Venue、Paper、CEX 行情/账户读取、OKX Demo 执行和链上安全适配 |
| `internal/data` | 合成/CEX 行情、指标、波动率和跨场所聚合 |
| `internal/runtime` | 启动对账、安全冻结、扫描、持仓监控和 symbol 锁 |
| `internal/persistence` | 内存、原子 JSON 文件和 PostgreSQL Repository |
| `internal/backtest` | 回测、扫参、walk-forward 和 PBO/DSR |
| `internal/ohlcv` | 已闭合 K 线校验、异步 PostgreSQL 归档、保留策略和停机补数 |
| `internal/events` | 有界发布订阅总线，供 SSE 消费 |
| `internal/metrics`、`internal/observability` | 运行指标、日志、轻量 trace |
| `internal/portfolio`、`internal/alerts` | 组合视图和告警输出 |

## 一轮交易的数据流

`POST /api/run` 快速返回 `run_id`，实际工作流在后台执行：

```text
采集 MarketSnapshot
  → 四类分析师并行分析（单维失败局部降级）
  → Strategist 生成 TradeProposal
  → Venue.Preflight
  → 确定性 Risk + RiskOfficer 评审
  → 人工审批或满足白名单/额度/风险阈值的 auto 审批
  → 对修改后的提案重新执行确定性风控
  → SafetyState 检查 Paper/OKX Demo/OKX 实盘执行配置、Kill Switch 和对账状态
  → 保存 OrderIntent
  → 当前执行场所 Place（client_id 幂等；成交后向交易所核实保护单）
  → Reviewer 复盘并保存 lessons
  → 发布 SSE 事件并保存最终结果
```

持久化检查点包括 `proposal`、`risk`、`approval`、`order_intent`、`execution` 和 `result`。同一设置更新或一组检查点通过批量接口原子提交，敏感字段在写入 Repository 前递归脱敏；仅保留最近 2000 个普通 run，`__` 前缀的系统状态不参与清理。

所有金额类契约使用 `internal/contracts.Decimal`。JSON 输出为十进制字符串，请勿在前端或集成层把金额先转为二进制浮点数再回传。

## 执行安全边界

当前执行能力如下：

| 场所 | 行情 | 账户只读 | 下单/撤单 |
| --- | --- | --- | --- |
| Paper | 支持 | 支持 | 支持 |
| Binance | 公共行情支持 | 配置凭据后支持签名只读请求 | 硬禁用 |
| OKX Demo | 公共行情支持 | Demo 凭据支持 | 仅 USDT 线性永续，需显式启用 |
| OKX 真实环境 | 公共行情支持 | 配置凭据后支持签名只读请求 | 仅 USDT 线性永续，需完整生产门禁显式启用 |
| Onchain | 适配器能力存在，未接入应用默认注册表 | 不作为当前运行路径 | 硬禁用 |

OKX 实盘不是单个环境变量能打开的功能，门禁分为三层：

- 静态配置：live、OKX、非 Demo、完整凭据与确认、API 鉴权、告警、PostgreSQL、OKX 实时行情、允许永续、强制逐仓、Kill Switch 关闭；
- 动态账户：官方时间偏差在安全阈值内、net mode、非 portfolio margin、API key 有 Trade 无 Withdraw 且绑定 IP；
- 进程所有权：取得并持续验证由 PostgreSQL session advisory lock 实现的账户级单实例租约；
- 构造 OKX 适配器时逐项校验，只有显式启用实盘的 OKX venue 才具有 `live + writable` 执行身份；
- Runtime mode policy 只允许 `paper/paper`、经适配器证明的 `paper/okx-demo`，以及 `live` 模式下经适配器证明的 `okx live writable` 目标；风险账本 scope 使用 `live:okx`，与 paper/demo 完全隔离；
- 运行中的设置更新拒绝把 mode 切到 `live`——实盘切换必须重启进程并重新通过启动对账；
- Binance、链上和未显式启用交易的 CEX `Place`/`Cancel` 明确返回禁用结果；
- 成交后向交易所核实保护单真实存在，核实失败进入有界补挂重试，耗尽后自动 reduce-only 紧急平仓、冻结新仓并发出 critical 告警；
- API、编排器、对账与监控共享同一个经过安全选择的执行 Venue。

仅设置 API Key 或 `CYP_LIVE_ACK=1` 都不会授权真实下单。

## 行情与 LLM 降级

- `CYP_DATA_SOURCE=synthetic`：默认使用确定性合成历史和实时 tick。
- `CYP_DATA_SOURCE=cex`：使用 `CYP_CEX_ID` 选择 Binance 或 OKX 公共行情；跨场所摘要由 MarketAggregator 聚合。
- 无有效 LLM Key：`llm.FromSettings` 使用 Mock provider，智能体执行规则化降级。
- 有 Key：支持 Anthropic，或使用 OpenAI-compatible 接口的 DeepSeek；调用受次数、token、成本和墙钟预算限制。
- 所有 Provider 通过统一 `UsageObserver` 输出安全元数据；报表按 provider/model、Agent、币种和自动/人工来源聚合，新增供应商无需修改统计链路。
- 单个分析师失败只把对应报告标为降级，保持报告顺序；父 context 取消会终止整组任务。

## 状态、对账与运行时

SafetyState 初始为冻结状态，只有成功的 `StartupReconcile` 可以解除冻结。对账目标是当前 Paper、OKX Demo 或 OKX 实盘执行场所，会把执行场所持仓、持久风险账本和订单事件日志双向修复，并核验保证金率与原生保护单；发现未解决差异时保持降级运行并冻结新仓。对账期间产生的新冻结具有更高代际，不能被旧结果覆盖；已有持仓的监控与持久 reduce-only 平仓保持运行。

RuntimeEngine 包含三条可选常驻循环：

- Scanner 按 `CYP_SCAN_INTERVAL` 扫描 `CYP_WATCHLIST` 并触发 run；
- PositionMonitor 按 `CYP_MONITOR_INTERVAL` 检查当前模拟执行场所的持仓、保护条件和告警。
- ExitManager 使用不调用 LLM 的数学状态机检查收益目标、回撤、时间止损和反向条件。

同一 symbol 通过锁避免并发扫描冲突。Kill Switch 阻止新开仓，但平仓接口仍可用于降低风险。

## 持久化

| `CYP_PERSISTENCE` | 行为 | 适用场景 |
| --- | --- | --- |
| `memory` | 仅进程内保存 | 单元测试、短时验证 |
| `file` | 原子写入 `CYP_STATE_FILE`，默认 `data/cyp-state.json` | 本地开发、单实例 |
| `postgres` | pgx 连接池，保存 `checkpoints` 和 `lessons` | Docker Compose、持久部署 |

文件模式通过临时文件、原子替换和 `.bak` 恢复降低中断写入风险；不支持多进程共享。PostgreSQL 模式会在启动时确保所需表和字段存在，初始化 SQL 同时位于 `db/migrations/` 与 `docker/initdb/`。各连接池限制连接数、生命周期、语句执行和锁等待时间，避免数据库阻塞无限占住运行线程。

OHLCV 归档与上述运行状态 Repository 解耦：即使 `CYP_PERSISTENCE=file`，也可通过 `CYP_OHLCV_ARCHIVE_ENABLED=true` 把行情写入同一 PostgreSQL/TimescaleDB。实时写入使用有界异步队列；失败只记录指标和日志，不阻塞交易、对账或主动平仓。启动及每 6 小时按 watchlist 修复默认 730 天窗口中的时间缺口，upsert 保证重复补数安全。

模型用量存储同样独立于运行状态 Repository。`llm_usage_events` 是 TimescaleDB 明细流，默认保留 90 天且从不写入 Prompt/响应内容；`llm_usage_daily` 按日期、时区、供应商、模型、Agent、币种、来源与状态做幂等长期聚合。每日预算门位于 LLM Provider 调用前，触顶不会停止 Scanner 之外的持仓监控、对账、保护单或 reduce-only 平仓路径。

## HTTP 与事件契约

核心接口包括健康/就绪、重新对账、持久订单审计、场所、设置、行情、持仓、组合、风控、指标、模型用量、回测、run、审批、Kill Switch 和 SSE。`GET /api/token-usage` 提供不含正文的趋势、维度和最近调用，`GET /api/audit/export` 导出脱敏订单/成交快照。公开业务契约与 Schema 见 [`api/openapi.yaml`](../api/openapi.yaml)，运行时另提供 `GET /api/ready` 作为安全就绪检查；Dashboard 事件见 [`api/jsonschema/dashboard-event.schema.json`](../api/jsonschema/dashboard-event.schema.json)。

SSE 使用 `text/event-stream`，连接建立后发送 retry 指令，每 15 秒发送 keepalive。前端请求 `replay=160`，总线在同一锁内完成订阅与有界历史预装，避免“先读历史、后订阅”之间丢事件；每帧使用纳秒时间戳 ID，浏览器携带 `Last-Event-ID` 时只续传缺失部分。事件仍只保留在当前进程内，后端重启后应重新拉取 REST 快照。

Dashboard 的 positions、risk 与 portfolio 共用 1 秒只读账户快照：余额、持仓和唯一币种标记价并发获取，同一轮前端刷新只访问交易所一次。下单路径、对账、自动退出和所有风控计算不读取该缓存。

Orchestrator 对异步 run 执行两级准入：每个币种最多一轮在途，全局等待数量有界；真正执行仍受 `MaxConcurrency` 信号量和共享 symbol lock 双重约束，人工平仓也使用同一把 symbol lock。这样扫描周期、重复点击或上游延迟都不能制造无界 goroutine 或同币种开平仓竞态。API 内存和持久化查询都只保留最近 2000 轮普通 run。

OKX（Demo 与实盘）下单 POST 不做盲重试；若连接中断或服务端错误导致提交结果未知，适配器使用确定性 `clOrdId` 主动查单并继续等待最终状态。只读 GET 对限流和瞬时服务错误做有界退避重试。任何平仓成功后都会再次确认空仓并清理残余保护单，确认或持久化失败则冻结后续新开仓。

写请求必须使用 JSON，并经过浏览器同源检查。配置 `CYP_API_TOKEN` 后还必须携带 Bearer token；Dashboard 只把令牌保存到当前标签会话并迁移删除旧的持久副本。非回环监听缺少 token 时进程拒绝启动。开发环境默认监听 `127.0.0.1`，对外部署仍须在可信反向代理后增加 TLS、访问控制和审计。回测最多同时执行两项，SSE 最多保持 64 个连接，请求 ID 只接受有限长度的安全字符。

## 变更规则

- 修改外部字段或事件时，同时更新 Go contracts、OpenAPI/JSON Schema、前端类型与契约测试。
- 修改风控、审批、对账或 Venue 时，必须补充失败路径和边界测试。
- 不得新增绕过 SafetyState、Risk、Approval 或执行场所门禁（Paper/OKX Demo/OKX 实盘生产门禁）的执行入口。
- 新持久化实现只通过 `persistence.Repository` 接入，不在 API handler 中直接访问数据库。
- 后台 goroutine 必须由 Application/Runtime/Orchestrator 持有，并有 context 取消与关闭路径。
