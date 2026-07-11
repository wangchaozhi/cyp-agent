# Go 运行时：对账、扫描与持仓监控

`internal/runtime` 协调三件事：启动对账、机会扫描和 Paper 持仓监控。v0.2.0 的 Runtime 只允许连接 `PaperVenue`；`ValidatePaperVenue` 会拒绝把非 Paper 场所接入对账或监控。

## 安全状态机

`SafetyState` 启动即冻结，且没有通用 `Unfreeze` 方法：

```text
进程启动
  → frozen: startup reconciliation required
  → BeginReconcile
  → frozen: reconciliation in progress
  ├─ 成功且 report.OK=true → 可开 Paper 新仓
  └─ 错误/差异/保护单缺口 → 保持 frozen
```

只有 `CompleteReconcile` 的成功路径可以解除冻结。每次新仓前，编排器和扫描器都会调用 `CheckNewPosition`，同时校验：

- `CYP_MODE=paper`；
- `CYP_EXECUTION_VENUE=paper`；
- Kill Switch 未开启；
- 对账不在进行且 SafetyState 未冻结。

任何一项不满足都 fail closed。`GET /api/ready` 返回 `ready`、`execution_ready`、`reconciling`、`safety` 和具体原因；`GET /api/health` 只表示进程存活，不能替代 readiness。

## 启动行为

应用组装位于 `internal/app/application.go`：

1. 加载并校验 `.env`/进程环境变量。
2. 创建 Paper、Binance、OKX 场所注册表；CEX 保持只读。
3. 创建数据源、仓储、事件总线、审批门和 Orchestrator。
4. 创建 `VenueReconciler`、`Scanner`、`PositionMonitor` 和 `Engine`。
5. 若 `CYP_RUNTIME_AUTOSTART=true`，`Engine.Start` 先对账再启动双循环。
6. 若为 `false`，仍调用一次 `StartupReconcile`，只是不会启动循环。
7. 对账失败时服务构建失败，不会进入可接收请求的状态。

当前对账读取 Paper 持仓，并验证每个有仓 symbol 是否存在有效的 reduce-only 止损保护单。报告包含 `positions`、`discrepancies`、`protective_gaps` 和 `ok`。

## 扫描循环

`Scanner` 按 `CYP_WATCHLIST` 顺序扫描，间隔由 `CYP_SCAN_INTERVAL`（秒）控制：

```text
每个 symbol
  → 检查 context
  → CheckNewPosition 安全门
  → SymbolLocks 防止同 symbol 重入
  → Orchestrator.Start(symbol)
  → 汇总错误并记录 scan 指标
```

watchlist 会去空白、去重并保留顺序。单个 symbol 失败不会阻止其他 symbol；一轮结束后用 `errors.Join` 汇总。当前应用为单进程内锁，多实例部署不能依靠它实现全局互斥。

配置示例：

```dotenv
CYP_RUNTIME_AUTOSTART=true
CYP_WATCHLIST=BTC/USDT,ETH/USDT
CYP_SCAN_INTERVAL=300
CYP_MAX_CONCURRENCY=2
```

`CYP_MAX_CONCURRENCY` 限制 Orchestrator 同时执行的 run 数。API 会先返回已接受，超过上限的 goroutine 等待信号量；当前没有持久任务队列，因此部署层仍应限制请求速率和排队规模。

## 持仓监控

`PositionMonitor` 每 `CYP_MONITOR_INTERVAL` 秒读取 Paper 持仓、报价、余额和保护单，检查：

- 场所是否提供原生保护单，当前持仓是否缺止损；
- mark price 是否有效；
- 相邻监控周期价格变动是否超过阈值；
- 当前价格是否逼近止损触发价；
- 永续价格是否逼近爆仓价；
- 永续名义仓位对应的保证金率是否逼近下限。

警报写结构化日志，发布 `position_monitor` SSE 事件，并可发送到 `CYP_ALERT_WEBHOOK`。Webhook 失败被隔离并计入指标，不会让监控 goroutine panic。

监控是告警层，不是止损执行器。Paper 原生保护单由 `PaperVenue` 模拟；未来真实场所必须依赖交易所原生保护单，不能把进程存活当成唯一保护。

## 检查点与恢复

Orchestrator 在 `proposal`、`risk`、`approval`、`order_intent`、`execution` 和最终 `result` 等阶段保存 JSON 检查点，并将复盘经验写入仓储。可选后端：

| `CYP_PERSISTENCE` | 特性 | 适用场景 |
| --- | --- | --- |
| `memory` | 进程退出即丢失 | 单元测试、临时演示 |
| `file` | 原子替换 `CYP_STATE_FILE`，崩溃时可恢复备份 | 默认单实例开发 |
| `postgres` | `pgx` 连接池和事务性 upsert | 容器、长时间运行、多读者 |

所有检查点在写入前递归屏蔽凭据、私钥、token、DSN 等敏感字段。当前代码会加载历史经验，但尚未实现从任意中间 checkpoint 自动继续执行；检查点用于审计和未来恢复状态机，不能宣称已有自动断点续跑。

## 事件与指标

运行阶段通过有界内存总线发布 SSE，包括 `reconciled`、`run_started`、`snapshot_ready`、`reports_ready`、`proposal_ready`、`risk_assessed`、`approval_decided`、`executed`、`reviewed`、`run_done` 和 `position_monitor`。

`GET /api/metrics` 汇总 run 状态、审批时延、滑点、执行成功率，以及扫描/监控/对账/Webhook 计数。事件总线只保存有界历史，不是持久审计日志。

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
