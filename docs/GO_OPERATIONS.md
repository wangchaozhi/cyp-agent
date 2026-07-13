# Go 后端运行手册

本手册适用于 `main` 当前纯 Go 后端。HTTP 契约见 [`api/openapi.yaml`](../api/openapi.yaml)，架构边界见 [`ARCHITECTURE.md`](ARCHITECTURE.md)。

## 安全边界

当前版本允许本地 Paper，以及显式配置的 OKX Demo 执行。默认安全组合为：

```dotenv
CYP_MODE=paper
CYP_EXECUTION_VENUE=paper
```

OKX Demo 执行需使用 Demo Trading 中单独创建的 API key，并设置 `CYP_MODE=paper`、`CYP_EXECUTION_VENUE=okx`、`CYP_OKX_DEMO=true` 和完整凭据；当前只支持 USDT 线性永续。Binance 和真实 OKX 下单仍硬禁用，`CYP_LIVE_ACK=1` 不会启用真实执行。

所有非 GET/HEAD/OPTIONS 请求都经过同源、JSON Content-Type 和可选 Bearer token 校验。回环监听可不配置 token；监听 `0.0.0.0`、局域网地址或容器地址时，`cyp-server` 强制要求 `CYP_API_TOKEN`，否则 fail-closed 退出。公网部署仍必须增加 TLS、访问控制和审计。

## 环境要求

- Go 1.25（准确版本以 `go.mod` 为准）；
- 开发仪表盘需要 Node.js 20.19+（或 22.12+）和 npm；
- 容器运行需要 Docker 与 Docker Compose；
- PostgreSQL 在 `CYP_PERSISTENCE=postgres` 或启用默认 OHLCV 归档时需要；不可用时 OHLCV 自动降级，但文件状态和交易风控仍可运行。

在 PowerShell 中先设置 UTF-8 并检查工具：

```powershell
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
go version
node --version
npm --version
```

## 最小启动

仓库根目录执行：

```powershell
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
if (-not (Test-Path -LiteralPath .env)) { Copy-Item .env.example .env }
go mod download
go run ./cmd/cyp-server -host 127.0.0.1 -port 8000
```

若 `.env` 已存在，不要用示例文件覆盖。默认配置使用合成行情、PaperVenue、Mock/规则 LLM 和 `data/cyp-state.json`，无需任何密钥。

服务启动时一定先对当前执行场所做对账。成功后才监听 HTTP；对账发现未解决差异或保护缺口时，进程会报错退出，不应通过删除检查绕过。

另开一个 PowerShell 验证：

```powershell
$health = Invoke-RestMethod http://127.0.0.1:8000/api/health
$ready = Invoke-RestMethod http://127.0.0.1:8000/api/ready
$health | ConvertTo-Json -Depth 10
$ready | ConvertTo-Json -Depth 10
```

正常 Paper 启动时，`health.ok=true`、`ready.ready=true` 且 `ready.safety.frozen=false`。`execution_ready=false` 通常表示 Kill Switch 已打开。

## 启动完整开发环境

根目录的一键脚本同时启动 Go API 和 Vite：

```powershell
./start-dev.bat
```

默认地址：

- 后端：`http://127.0.0.1:8000`；
- 前端：`http://127.0.0.1:5173`；
- 日志：`.logs/backend.*.log` 与 `.logs/frontend.*.log`。

常用参数：

```powershell
./start-dev.bat -BackendPort 8001 -FrontendPort 5174
./start-dev.bat -SkipInstall
./start-dev.bat -NoKill
```

脚本默认结束占用目标端口的旧进程；`-NoKill` 会保留它们，并在端口冲突时失败。`-SkipInstall` 仅在 `apps/web/node_modules` 已准备好时使用。

也可先构建前端，再由 Go 服务直接托管静态文件：

```powershell
Push-Location apps/web
npm ci
npm run build
Pop-Location
go run ./cmd/cyp-server -host 127.0.0.1 -port 8000 -web-dir apps/web/dist
```

## 构建与测试

本地基础验证：

```powershell
$unformatted = gofmt -l cmd internal
if ($unformatted) { $unformatted; throw "Go files need gofmt" }
go vet ./...
go test -count=1 ./...
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -o ./bin/cyp-server.exe ./cmd/cyp-server
go build -trimpath -o ./bin/cyp.exe ./cmd/cyp
```

前端验证：

```powershell
Push-Location apps/web
npm ci
npm audit --audit-level=high
npx --yes @redocly/cli@2.38.0 lint ../../api/openapi.yaml
npm run typecheck
npm run build
Pop-Location
```

CI 在 Linux 上额外执行 `go test -race -count=1 ./...`、OpenAPI lint 和 Docker 镜像构建。

## CLI

直接运行源码：

```powershell
go run ./cmd/cyp version
go run ./cmd/cyp config
go run ./cmd/cyp backtest -symbol BTC/USDT -bars 300 -window 60
go run ./cmd/cyp backtest -json
go run ./cmd/cyp sweep -thresholds 0.08,0.12,0.18 -top 5
```

`cyp config` 只输出脱敏快照，不输出 API Key 或数据库 DSN。`backtest` 默认使用可复现的合成数据；HTTP 回测可将 `data` 设为 `cex` 拉取所选 CEX 的公共历史 K 线。

## 配置规则

配置优先级为：进程环境变量 > `.env` > Go 默认值。启动时会严格校验枚举、布尔值、数值范围和必填字段；无效配置会让服务直接退出。

常用配置：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CYP_MODE` | `paper` | 当前仅 Paper 可执行 |
| `CYP_EXECUTION_VENUE` | `paper` | 执行场所；非 Paper 不会下单 |
| `CYP_DATA_SOURCE` | `synthetic` | `synthetic` 或 `cex` |
| `CYP_CEX_ID` | `binance` | 公共/只读行情选择 `binance` 或 `okx` |
| `CYP_APPROVAL` | `dashboard` | 人工审批进入 pending 队列（`cli` 为废弃别名，等同 `dashboard`）；`auto` 受白名单、风险和额度限制 |
| `CYP_KILL` | `0` | 开启后阻止新仓，仍允许平仓 |
| `CYP_RUNTIME_AUTOSTART` | `false` | 是否启动扫描和持仓监控循环 |
| `CYP_WATCHLIST` | `BTC/USDT` | 扫描标的，逗号分隔 |
| `CYP_PERSISTENCE` | `file` | `memory`、`file` 或 `postgres` |
| `CYP_STATE_FILE` | `data/cyp-state.json` | 文件持久化路径 |
| `CYP_DB_URL` | 本地 5433 | PostgreSQL DSN，供状态或 OHLCV 归档使用 |
| `CYP_OHLCV_ARCHIVE_ENABLED` | `true` | 异步归档、停机缺口补录和历史复用 |
| `CYP_OHLCV_RETENTION_DAYS` | `730` | K 线保留天数，范围 30–3650 |
| `CYP_TOKEN_USAGE_ENABLED` | `true` | 多供应商模型调用统计和自然日预算门 |
| `CYP_TOKEN_USAGE_RETENTION_DAYS` | `90` | 调用明细保留天数；每日聚合不随明细清理 |
| `CYP_TOKEN_USAGE_TIMEZONE` | `Asia/Shanghai` | 自然日预算与报表时区 |
| `CYP_DAILY_TOKEN_BUDGET` | `2000000` | 全部 run 共用的每日 Token 上限 |
| `CYP_DAILY_COST_BUDGET_USD` | `50` | 全部 run 共用的每日估算成本上限 |
| `CYP_LOG_LEVEL` | `INFO` | `DEBUG`、`INFO`、`WARN`、`ERROR` |
| `CYP_API_TOKEN` | 空 | 非回环监听必填；保护所有写请求 |

风险和预算变量完整清单见 `.env.example`。修改 `.env` 后需重启服务；`POST /api/settings` 只更新当前进程允许的 LLM 设置，且响应始终脱敏。`CYP_LLM_BASE_URL` 是启动期配置，不能通过 HTTP 修改。

## 持久化选择

### 文件模式

```dotenv
CYP_PERSISTENCE=file
CYP_STATE_FILE=data/cyp-state.json
```

这是默认模式，适合单实例。写入使用原子替换；异常中断时启动逻辑会处理同目录的 `.bak`。不要让两个进程同时写同一个状态文件，也不要手工编辑运行中的文件。

### 内存模式

```dotenv
CYP_PERSISTENCE=memory
```

仅适合测试和一次性演示，进程退出后检查点与 lessons 丢失。

### PostgreSQL 模式

```powershell
docker compose up -d db
```

`.env` 配置：

```dotenv
CYP_PERSISTENCE=postgres
CYP_DB_URL=postgresql://cyp:cyp@localhost:5433/cyp
```

服务启动时会连接、Ping 并确保 `checkpoints`、`lessons` 所需结构存在；连接失败时不会静默退回内存。

### 独立 OHLCV 归档

运行状态可以继续使用默认文件模式，同时单独启用时序归档：

```dotenv
CYP_PERSISTENCE=file
CYP_DB_URL=postgresql://cyp:cyp@localhost:5433/cyp
CYP_OHLCV_ARCHIVE_ENABLED=true
CYP_OHLCV_RETENTION_DAYS=730
```

归档启动失败会记录 `ohlcv_archive_unavailable` 并让交易继续。成功后只异步写入已闭合且校验通过的 K 线；启动与每 6 小时执行缺口补录，每天清理超过保留期的数据。可通过 `/api/metrics` 的 `ohlcv_*` 计数和 PostgreSQL 查询核验：

```sql
SELECT venue, symbol, timeframe, count(*), min(ts), max(ts)
FROM ohlcv
WHERE quality_status = 'validated'
GROUP BY venue, symbol, timeframe
ORDER BY venue, symbol, timeframe;
```

### 多供应商模型用量

默认使用同一 PostgreSQL 建立 `llm_usage_events` 和 `llm_usage_daily`。调用完成后异步写入 provider、model、Agent、币种、run、自动/人工来源、token、估算标志、成本、耗时与状态，不写 Prompt 和回复正文。Dashboard 或 API 可查询：

```text
GET /api/token-usage?days=7&bucket=hour&limit=50
```

70% 与 90% 预算水位产生 `token_budget_alert`，100% 拒绝新的 LLM Provider 调用。该门不在 Venue、PositionMonitor 或 AutomatedExitManager 路径上，所以触顶后持仓保护和平仓仍继续。数据库不可用时统计降级到进程内且交易继续；应根据 `token_usage_store_unavailable` 日志恢复数据库，避免重启后丢失降级期间明细。

## 容器运行

启动数据库和后端：

```powershell
$env:CYP_API_TOKEN = "<高强度随机值>"
docker compose up --build -d
docker compose ps
docker compose logs -f backend
```

Compose 默认使用 Paper、合成行情、Dashboard 审批和 PostgreSQL 持久化，只把服务映射到主机回环地址的 `8000` 端口。容器内监听非回环地址，因此令牌必填；Dashboard 设置页中的“API 写操作令牌”只保存在当前浏览器。停止但保留数据库卷：

```powershell
docker compose down
```

不要在没有备份的情况下附加 `-v`，该参数会删除数据库卷。

## 手工闭环验证

触发一轮：

```powershell
$base = "http://127.0.0.1:8000"
$run = Invoke-RestMethod -Method Post -Uri "$base/api/run" -ContentType "application/json" -Body '{"symbol":"BTC/USDT"}'
$run | ConvertTo-Json
```

观察状态和待审批项：

```powershell
Invoke-RestMethod "$base/api/runs/$($run.run_id)" | ConvertTo-Json -Depth 20
Invoke-RestMethod "$base/api/pending" | ConvertTo-Json -Depth 20
```

只有产生非 `flat` 且通过风控的提案才会进入 pending。批准：

```powershell
$body = @{ decision = "approve"; operator = "manual-smoke"; note = "Paper 验证" } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri "$base/api/approvals/$($run.run_id)" -ContentType "application/json" -Body $body
```

查看结果、持仓与事件：

```powershell
Invoke-RestMethod "$base/api/runs/$($run.run_id)" | ConvertTo-Json -Depth 20
Invoke-RestMethod "$base/api/positions" | ConvertTo-Json -Depth 20
curl.exe -N "$base/api/events"
```

SSE 只发送连接后的实时事件；断线重连后应通过 REST 重新读取 run、pending 和 positions 快照。

## Kill Switch 与平仓

开启 Kill Switch：

```powershell
$base = "http://127.0.0.1:8000"
Invoke-RestMethod -Method Post -Uri "$base/api/killswitch" -ContentType "application/json" -Body '{"on":true}'
```

关闭：

```powershell
Invoke-RestMethod -Method Post -Uri "$base/api/killswitch" -ContentType "application/json" -Body '{"on":false}'
```

Paper 平仓不会被 Kill Switch 阻止：

```powershell
$close = @{ symbol = "BTC/USDT"; instrument = "spot" } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri "$base/api/positions/close" -ContentType "application/json" -Body $close
```

Kill Switch 是进程内运行控制，不替代停止服务、权限控制或事故处置流程。

## 运行时与故障定位

启用自动扫描和监控：

```dotenv
CYP_RUNTIME_AUTOSTART=true
CYP_SCAN_INTERVAL=600
CYP_MONITOR_INTERVAL=5
CYP_WATCHLIST=BTC/USDT,ETH/USDT
CYP_MAX_CONCURRENCY=2
```

默认每 10 分钟扫描一次。Dashboard 设置页可选 1/5/10/15/30 分钟，保存后立即生效并持久化；`CYP_MONITOR_INTERVAL=5` 是持仓风控检测，不调用 LLM。

排查顺序：

1. 查看进程退出错误或 `.logs/backend.err.log`；
2. 检查 `/api/health` 与 `/api/ready`；
3. 检查 `ready.safety.reason`、`reconciling` 和 Kill Switch；
4. 检查 `.env` 中 mode、venue、persistence 和 DSN；
5. PostgreSQL 模式检查 `docker compose ps` 与 `docker compose logs db`；
6. CEX 行情异常时切回 `CYP_DATA_SOURCE=synthetic` 验证核心链路；
7. LLM 异常时移除无效 Key，确认 Mock/规则降级链路可用。

常见启动失败应直接修复根因，不要改为静默降级：

- 状态文件 JSON 损坏或权限不足；
- PostgreSQL 连接/Ping 失败；
- 启动对账未通过；
- 配置枚举或数值非法；
- 监听端口被占用。

## 停止服务

前台运行时按 `Ctrl+C`。服务会取消后台任务、唤醒审批和 SSE 等待者，并在 10 秒期限内关闭 HTTP。

若使用开发脚本启动了后台进程，可再次运行脚本让它清理默认端口，或按监听端口定位进程后停止：

```powershell
Get-NetTCPConnection -LocalPort 8000,5173 -State Listen |
  Select-Object -ExpandProperty OwningProcess -Unique |
  ForEach-Object { Stop-Process -Id $_ }
```

停止前优先打开 Kill Switch，并确认没有仍待处理的 Paper 审批或持仓。
