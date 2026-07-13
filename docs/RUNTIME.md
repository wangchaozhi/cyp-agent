# Go 运行时：对账、扫描与持仓监控

`internal/runtime` 协调启动对账、机会扫描、只读持仓监控和主动退出。Runtime 只允许连接本地 `PaperVenue`，或能显式证明 `DemoTradingEnabled` 的 OKX Demo 适配器；其他 CEX 在读取持仓前即被拒绝。

## 安全状态机

`SafetyState` 启动即冻结，且没有通用 `Unfreeze` 方法：

```text
进程启动
  → frozen: startup reconciliation required
  → BeginReconcile
  → frozen: reconciliation in progress
  ├─ 成功且 report.OK=true → 可在已选模拟场所开新仓
  └─ 错误/差异/保护单缺口 → 保持 frozen
```

只有 `CompleteReconcile` 的成功路径可以解除冻结。每次新仓前，编排器和扫描器都会调用 `CheckNewPosition`，同时校验：

- `CYP_MODE=paper`；
- `CYP_EXECUTION_VENUE=paper`，或完整配置的 `okx` Demo 执行路径；
- Kill Switch 未开启；
- 对账不在进行且 SafetyState 未冻结。

任何一项不满足都 fail closed。`GET /api/ready` 返回 `ready`、`execution_ready`、`reconciling`、`safety` 和具体原因；`GET /api/health` 只表示进程存活，不能替代 readiness。

## 启动行为

应用组装位于 `internal/app/application.go`：

1. 加载并校验 `.env`/进程环境变量。
2. 创建 Paper、Binance、OKX 场所注册表；只把 Paper 或显式启用的 OKX Demo 接到执行链。
3. 创建数据源、仓储、事件总线、审批门、多供应商模型用量 Tracker 和 Orchestrator。
4. 创建 `VenueReconciler`、`Scanner`、`PositionMonitor`、`AutomatedExitManager` 和 `Engine`。
5. 若 `CYP_RUNTIME_AUTOSTART=true` 或自动化总开关开启，`Engine.Start` 先对账再启动后台循环。
6. 两者都关闭时仍调用一次 `StartupReconcile`，只是不会启动循环；之后从 Dashboard 开启自动化会安全地启动运行时。
7. 对账有差异时保持 SafetyState 冻结，API 与减仓路径仍可用于检查和处置已有持仓。

## K 线归档与停机补数

默认启用独立 PostgreSQL OHLCV 归档并保留 730 天。实时 CEX 快照先完成交易分析，再把已闭合且校验通过的 1 小时 K 线投递到有界异步队列；PostgreSQL 超时或不可用不会阻塞扫描、对账、监控和平仓。

软件停机期间确实不会实时采集，因此 Backfiller 会在每次启动立即比较数据库时间点与完整保留窗口，并按缺口分页向 Binance/OKX 补录；之后每 6 小时重复检查。唯一键 `(venue, symbol, timeframe, ts)` 使重复补录幂等，交易所临时失败的缺口会留到下一轮重试。每天按 `CYP_OHLCV_RETENTION_DAYS` 清理过期数据，默认 730 天可覆盖两年季节性和不同市场状态，同时对 1 小时 K 线保持较小存储量。

对账读取当前模拟执行场所的持仓，并验证每个有仓 symbol 是否存在有效的 reduce-only 止损保护单；OKX Demo 通过私有待处理策略订单接口核验。报告包含 `positions`、`discrepancies`、`protective_gaps` 和 `ok`。

## 扫描循环

`Scanner` 按当前 Dashboard watchlist 顺序扫描，默认间隔由 `CYP_SCAN_INTERVAL=600`（10 分钟）控制；设置页可在 1/5/10/15/30 分钟之间切换。修改会持久化、立即重置调度定时器并作用于后续周期，无需重启，也不会因保存设置额外触发一轮 LLM 分析。自动化总开关或“定时扫描”子开关关闭时整轮为空操作：

```text
每个 symbol
  → 检查 context
  → CheckNewPosition 安全门
  → SymbolLocks 防止同 symbol 重入
  → Orchestrator.Start(symbol)
  → 汇总错误并记录 scan 指标
```

watchlist 会去空白、去重并保留顺序。单个 symbol 失败不会阻止其他 symbol；一轮结束后用 `errors.Join` 汇总。当前应用为单进程内锁，多实例部署不能依靠它实现全局互斥。

扫描间隔直接决定每天发起的币种分析轮次：`86400 ÷ 间隔秒数 × watchlist 币种数`。以 7 个币种为例，默认 10 分钟约为 1008 个币种分析轮次/天，约为 1 分钟档的 10%。这是调用轮次估算，实际 Token 还取决于模型、上下文和失败重试。

配置示例：

```dotenv
CYP_RUNTIME_AUTOSTART=true
CYP_AUTOMATION_ENABLED=true
CYP_WATCHLIST=BTC/USDT,ETH/USDT
CYP_SCAN_INTERVAL=600
CYP_MAX_CONCURRENCY=2
```

`CYP_MAX_CONCURRENCY` 限制 Orchestrator 同时执行的 run 数。API 会先返回已接受，超过上限的 goroutine 等待信号量；当前没有持久任务队列，因此部署层仍应限制请求速率和排队规模。

## 模型调用预算与统计

自动扫描使用 `source=automatic`，Dashboard 手动触发使用 `source=manual`；Strategist 与 RiskOfficer 各自写入 Agent 归因。每次调用由 Provider 返回实际输入/输出 Token，供应商缺失 usage 时才使用保守估算并设置 `token_estimated=true`；成本无法由供应商直接提供时设置 `cost_estimated=true`。

自然日预算使用 `CYP_TOKEN_USAGE_TIMEZONE`（默认 `Asia/Shanghai`）切日。70% 提醒、90% 严重告警、100% 暂停新模型调用；暂停状态在下一自然日自动解除。LLM 失败或预算触顶时 Agent 回退确定性逻辑，不会关闭 PositionMonitor、AutomatedExitManager、对账或交易所原生保护单。

## 持仓监控

`PositionMonitor` 默认每 `CYP_MONITOR_INTERVAL=5` 秒读取当前模拟执行场所的持仓、报价、余额和保护单，检查：

- 场所是否提供原生保护单，当前持仓是否缺止损；
- mark price 是否有效；
- 相邻监控周期价格变动是否超过阈值；
- 当前价格是否逼近止损触发价；
- 永续价格是否逼近爆仓价；
- 永续名义仓位对应的保证金率是否逼近下限。

警报写结构化日志，发布 `position_monitor` SSE 事件，并可发送到 `CYP_ALERT_WEBHOOK`。Webhook 失败被隔离并计入指标，不会让监控 goroutine panic。

监控是告警层，不是止损执行器。Paper 保护单由 `PaperVenue` 模拟；OKX Demo 下单时附加交易所原生止盈止损，不能把进程存活当成唯一保护。

## 策略自动化

自动化配置可通过顶部总开关或设置页运行时修改，并写入同一持久化仓储。默认全部开启，总开关控制六个独立策略：

- 定时扫描：按 watchlist 触发分析；
- 自动开仓：以置信度作为保守胜率代理，先计算 `EV = p × RR - (1-p)` 和 Kelly 比例 `EV / RR`，再使用 `min(单笔风险上限, Kelly 比例 × Kelly 使用比例)` 作为风险预算；名义仓位为 `账户权益 × 风险预算 ÷ 止损距离比例`，并受策略仓位、风险引擎调整、最小/最大金额共同约束；
- 自动加仓：仅在同向仓位已有浮盈且信号继续通过时采用 `risk_add(k) = max_risk_per_trade × add_risk_decay^k` 的递减风险预算；每次金额还受现仓比例、冷却时间、最多次数、单仓/组合敞口和聚合保证金约束。加仓不会降低现有逐仓杠杆；若当前杠杆高于新的波动安全上限，直接放弃加仓；
- 数学审批：白名单、风险分、最低置信度、最低盈亏比、正期望和正 Kelly 必须同时通过；
- 主动退出：理想收益达到 `profit_target_r`、行情恶化至 `-loss_cut_r`、EWMA 动态跟踪底线被击穿或最长持仓时间止损任一成立时退出；价格距离全部按开仓价到原始止损的距离归一化为 `R`，且连续命中配置次数后才执行。
- 自动反向：相反方向信号必须在窗口内连续确认，且满足更高的置信度和盈亏比阈值；随后按 `reduce-only 平旧仓 → 核验归零 → 撤销残余保护单 → 重新读取权益与持仓 → 再次风控 → 开反向仓并挂新保护单` 执行。冷却时间和每日次数上限抑制来回打脸；任何步骤失败都会停止后续开仓。

主动退出只对能核验有效 reduce-only 止损的仓位工作，最终只发送 reduce-only 市价单。自动加仓、扫描与退出共享标的锁，不会对同一币种并发开平仓。自动反向不会使用普通反向订单直接冲销旧仓。Live 只读模式不能开启自动化。关闭总开关会停止扫描、自动开仓、自动加仓、自动审批、主动退出和自动反向，但不会撤销或关闭交易所侧原生止损止盈。

## 检查点与恢复

Orchestrator 在 `proposal`、`risk`、`approval`、`order_intent`、`execution` 和最终 `result` 等阶段保存 JSON 检查点，并将复盘经验写入仓储。可选后端：

| `CYP_PERSISTENCE` | 特性 | 适用场景 |
| --- | --- | --- |
| `memory` | 进程退出即丢失 | 单元测试、临时演示 |
| `file` | 原子替换 `CYP_STATE_FILE`，崩溃时可恢复备份 | 默认单实例开发 |
| `postgres` | `pgx` 连接池和事务性 upsert | 容器、长时间运行、多读者 |

所有检查点在写入前递归屏蔽凭据、私钥、token、DSN 等敏感字段。当前代码会加载历史经验，但尚未实现从任意中间 checkpoint 自动继续执行；检查点用于审计和未来恢复状态机，不能宣称已有自动断点续跑。

## 事件与指标

运行阶段通过有界内存总线发布 SSE，包括 `reconciled`、`run_started`、`snapshot_ready`、`reports_ready`、`proposal_ready`、`risk_assessed`、`approval_decided`、`executed`、`reviewed`、`run_done`、`position_monitor`、`automation_evaluated`、`automated_exit`、`reversal_observed`、`reversal_closed`、`reversal_reassessed` 和 `reversal_opened`。

`GET /api/metrics` 汇总 run 状态、审批时延、滑点、执行成功率，以及扫描/监控/对账/Webhook 和 OHLCV 排队、保存、丢弃、错误、清理、补数计数。事件总线只保存有界历史，不是持久审计日志。

## 停机

`cyp-server` 监听 `SIGINT`/`SIGTERM`：

1. 取消根 context。
2. 停止 Runtime 循环并等待 goroutine。
3. 关闭 Orchestrator，唤醒审批等待者。
4. 关闭事件总线，结束 SSE 客户端。
5. 关闭 Repository 和所有 CEX HTTP 资源。
6. 在 10 秒期限内执行 HTTP graceful shutdown，超时则强制关闭。

## 故障响应

| 故障 | 当前行为 | 操作 |
| --- | --- | --- |
| 启动对账失败 | 应用启动失败/保持冻结 | 查看结构化日志，修复 Paper 状态或保护单后重启 |
| 数据源失败 | Orchestrator 尝试合成行情降级 | 检查 `CYP_DATA_SOURCE` 和网络 |
| 单个 Agent 失败 | 该报告标记 degraded | 查看报告理由，其他维度继续 |
| 审批超时 | fail-safe 拒绝 | 重新发起 run，不重放旧订单 |
| 仓储写失败 | 当前 run 失败，不继续执行 | 修复文件权限/数据库后重新分析 |
| Kill Switch 开启 | 拒绝新仓 | 确认风险事件后人工关闭；平仓仍可用 |
| 进程崩溃 | 已写检查点保留，未自动续跑 | 核对持仓和检查点后启动新 run |

## 验证

```bash
go test -race ./internal/runtime ./internal/orchestrator ./internal/persistence
go run ./cmd/cyp-server

curl http://127.0.0.1:8000/api/ready
curl http://127.0.0.1:8000/api/metrics
```

Runtime 改动必须覆盖：对账失败保持冻结、Kill/非 Paper 拒绝新仓、循环取消无泄漏、单 symbol 失败隔离、保护单缺口告警、仓储失败时不执行。
