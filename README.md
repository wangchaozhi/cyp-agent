# cyp-agent

cyp-agent 是一个以风控为先、人在环的加密资产多智能体交易助手。当前 `main` 的后端已全部重构为 Go，前端为 React + Vite；默认使用确定性的合成行情和 `PaperVenue`，不需要交易所密钥即可完成「采集 → 分析 → 决策 → 风控 → 审批 → 模拟执行 → 复盘」闭环。

> 安全边界：默认配置不发出任何真实订单。OKX 实盘只开放 USDT 线性永续，并要求显式 live/OKX/非 Demo 配置、完整凭据与确认、API 鉴权、告警、PostgreSQL、OKX 实时行情、永续与逐仓限制以及关闭 Kill Switch；启动时还会核验服务器时钟、账户模式/等级、API 权限与 IP 白名单，并取得账户级单实例执行租约。任何一项失败都会拒绝启动或拒绝交易所写操作。Binance 与链上真实下单保持硬禁用；运行中的进程不能切换到 `live`。上线前请通读 [docs/GO_OPERATIONS.md](docs/GO_OPERATIONS.md)。

## 当前能力

| 模块 | 状态 |
| --- | --- |
| Go 服务 | `net/http` REST、SSE、React 静态资源、优雅停机 |
| 多智能体 | 技术面、衍生品、情绪、链上分析并行；策略官、风控官、复盘官串行 |
| 风控 | 确定性硬护栏、Kill Switch、审批后重新校验、启动账本/持仓对账、保护缺口冻结门 |
| 执行 | Paper 现货/永续模拟撮合；经生产门禁启用的 OKX Demo/实盘 USDT 永续下单、部分成交撤余、未知提交查单恢复、原生止盈止损核验、保护单补挂与紧急平仓 |
| 数据 | 合成行情；Binance/OKX 只读行情；已闭合 K 线异步归档、停机补数与缺口修复 |
| 持久化 | 运行状态支持内存/原子 JSON/PostgreSQL；检查点有界保留；OHLCV 可独立归档到 TimescaleDB |
| 运行时 | 启动对账、watchlist 扫描、持仓监控、告警和运行指标 |
| 回测 | 确定性合成回测、扫参、OOS、PBO、PSR/DSR、walk-forward、purged K-fold |
| 前端 | React 18 + TypeScript + Vite，覆盖事件、审批、持仓、风险、市场和回测 |

## 技术与目录

- Go 1.25：服务、领域契约、Agent、风控、场所、回测、持久化和运行时。
- React 18 / TypeScript / Vite：`apps/web` 仪表盘。
- PostgreSQL / TimescaleDB：可选的运行检查点、经验和 OHLCV 归档。

```text
cmd/cyp-server/       REST/SSE 服务与 React 静态资源入口
cmd/cyp/              config、backtest、sweep、flatten、version CLI
internal/contracts/   精确 Decimal 与领域/API 契约
internal/agents/      分析师、策略官、风控官、复盘官
internal/orchestrator/完整交易闭环编排
internal/risk/        确定性硬风控
internal/venue/       Paper、Binance、OKX、链上安全骨架
internal/runtime/     对账、扫描、持仓监控
internal/backtest/    回测与统计检验
internal/ohlcv/       K 线校验、异步归档、保留与停机补数
internal/persistence/ memory/file/PostgreSQL 仓储
internal/api/         REST、SSE 与静态资源处理
apps/web/             React 仪表盘
api/                  OpenAPI 与 JSON Schema
db/migrations/        PostgreSQL 迁移
```

## 快速开始

要求：Go 1.25+；项目会自动选择已修复标准库漏洞的 Go 1.26.5 工具链。开发仪表盘还需要 Node.js 20.19+（或 22.12+）；启用 PostgreSQL 状态持久化或默认 OHLCV 归档时需要可访问的 PostgreSQL/TimescaleDB。

```powershell
Copy-Item .env.example .env
go run ./cmd/cyp-server -host 127.0.0.1 -port 8000
```

服务启动后访问 `http://127.0.0.1:8000/api/health`，并用 `/api/ready` 判断能否新开仓。默认配置使用 `data/cyp-state.json` 持久化状态，首次写入检查点或经验时会自动创建目录和文件。

Windows 下可同时启动后端和 Vite 开发服务器：

```powershell
.\start-dev.bat

# 可选：指定端口，或保留已有端口进程
.\start-dev.bat -BackendPort 8001 -FrontendPort 5174
.\start-dev.bat -NoKill

# 只有明确需要隔离实例时才允许多个后端；默认会拒绝，防止重复自动扫描/下单
.\start-dev.bat -BackendPort 8001 -FrontendPort 5174 -AllowMultipleBackends
```

其他平台可分别启动：

```bash
go run ./cmd/cyp-server -host 127.0.0.1 -port 8000

cd apps/web
npm ci
VITE_BACKEND_URL=http://127.0.0.1:8000 npm run dev
```

构建前端后，Go 服务会直接托管 `apps/web/dist`：

```bash
cd apps/web
npm ci
npm run build
cd ../..
go run ./cmd/cyp-server -web-dir apps/web/dist
```

## 使用闭环

默认审批通道仍为 `dashboard`（历史值 `cli` 已废弃，作为其别名继续兼容），但默认开启数学自动审批：满足币种白名单、风险、金额、置信度、盈亏比和 Kelly 正期望边界时自动批准，否则自动拒绝，不进入人工队列。只有关闭“数学自动审批”后，提案才会等待仪表盘或 API 批准、拒绝或改单。可通过仪表盘操作，或直接调用 API：

```bash
# 发起一轮分析
curl -X POST http://127.0.0.1:8000/api/run \
  -H "Content-Type: application/json" \
  -d '{"symbol":"BTC/USDT"}'

# 查询待审批项
curl http://127.0.0.1:8000/api/pending
```

关键端点：

| 端点 | 说明 |
| --- | --- |
| `GET /api/health`、`GET /api/ready` | 存活状态、执行就绪和对账冻结原因 |
| `POST /api/run`、`GET /api/runs/{run_id}` | 启动和查询一次闭环 |
| `GET /api/events?replay=160` | SSE 实时事件流；支持有界历史回放和断线续传 |
| `GET /api/pending` | 待人工审批列表 |
| `POST /api/approvals/{run_id}` | 批准、拒绝或修改提案 |
| `GET /api/positions`、`POST /api/positions/close` | 当前执行场所的持仓与平仓 |
| `GET /api/risk`、`GET /api/portfolio` | 风控和组合视图 |
| `GET /api/trades` | 持久化开平仓账本 |
| `GET /api/market`、`GET /api/venues` | 市场聚合与场所能力 |
| `POST /api/backtest` | 合成或历史 K 线回测 |
| `GET /api/metrics` | 闭环与运行时指标 |
| `GET/POST /api/killswitch` | 查询或切换 Kill Switch |

完整契约见 [api/openapi.yaml](api/openapi.yaml)。

## CLI 与回测

```bash
# 查看脱敏配置与版本
go run ./cmd/cyp config
go run ./cmd/cyp version

# 可复现的合成行情回测
go run ./cmd/cyp backtest --symbol BTC/USDT --bars 300 --window 60

# 扫参，并输出样本外收益、PBO、Deflated Sharpe 与结论
go run ./cmd/cyp sweep --symbol BTC/USDT --bars 300 --top 5
```

`cyp backtest` 当前 CLI 只运行合成数据。服务端 `POST /api/backtest` 可分页拉取真实历史 K 线，并在启用时复用 PostgreSQL 归档；回测本身从不下单。

## 配置

配置优先级为进程环境变量高于 `.env`，完整清单见 [.env.example](.env.example)。关键项如下：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CYP_MODE` | `paper` | `paper` 或 `live`；`live` 需满足完整 OKX 实盘门禁，且不能在运行中切换 |
| `CYP_EXECUTION_VENUE` | `paper` | `paper` 或 `okx`；`okx` 支持 Demo 或显式启用的实盘，`binance` 执行硬禁用 |
| `CYP_DATA_SOURCE` | `synthetic` | `synthetic` 或 `cex` |
| `CYP_OKX_REGION` | `global` | OKX 账户注册区域：`global`、`us` 或 `eea`；映射到固定官方 API 域名 |
| `CYP_APPROVAL` | `dashboard` | `dashboard` 或兼容旧配置的 `auto`；`cli` 是废弃别名 |
| `CYP_KILL` | `0` | 开启后拒绝所有新仓，仍允许减仓/平仓路径 |
| `CYP_AUTOMATION_ENABLED` | `true` | 自动扫描、分数 Kelly 开仓、盈利递减加仓、数学审批、主动退出和受控反向的总开关；可在 Dashboard 运行时切换 |
| `CYP_AUTO_ENTRY` / `CYP_AUTO_ADD` / `CYP_AUTO_REVERSE` | `true` / `true` / `true` | 自动开仓、加仓与反向独立开关；所有自动化默认开启，仍受确定性风控约束 |
| `CYP_RUNTIME_AUTOSTART` | `false` | 启动持仓监控等运行时循环；自动扫描仍受自动化开关控制 |
| `CYP_PERSISTENCE` | `file` | `memory`、`file` 或 `postgres` |
| `CYP_STATE_FILE` | `data/cyp-state.json` | 原子 JSON 状态文件 |
| `CYP_DB_URL` | 本地 `5433/cyp` | PostgreSQL DSN，供状态持久化或独立 OHLCV 归档使用 |
| `CYP_OHLCV_ARCHIVE_ENABLED` | `true` | 异步保存已闭合 K 线，并启动缺口补录；数据库故障时交易继续 |
| `CYP_OHLCV_RETENTION_DAYS` | `730` | 时序历史默认保留天数，允许 30–3650 |
| `CYP_LLM_PROVIDER` | `anthropic` | `anthropic` 或 `deepseek`；缺 key 时走规则降级 |
| `CYP_TOKEN_USAGE_ENABLED` | `true` | 记录多供应商模型调用元数据并提供 Dashboard 趋势/明细；不保存 Prompt 或回复正文 |
| `CYP_TOKEN_USAGE_RETENTION_DAYS` | `90` | PostgreSQL 调用明细保留天数；每日供应商/模型聚合长期保留 |
| `CYP_DAILY_TOKEN_BUDGET` | `2000000` | 跨 run 的自然日 Token 上限；100% 后仅暂停新 LLM 分析 |
| `CYP_DAILY_COST_BUDGET_USD` | `50` | 自然日估算成本上限（美元） |
| `CYP_API_TOKEN` | 空 | 非回环监听必填；认证所有写请求 |
| `CYP_CORS_ORIGINS` | `http://127.0.0.1:5173,http://localhost:5173` | 允许跨域访问的前端来源，多个来源用逗号分隔 |

使用 OKX Demo 执行时，还需设置 `CYP_ALLOW_PERP=1`、`CYP_DATA_SOURCE=cex`、`CYP_CEX_ID=okx` 和 `CYP_OKX_DEMO=true`，并将 `OKX_API_KEY`、`OKX_API_SECRET`、`OKX_PASSWORD` 填为在 OKX Demo Trading 中单独创建的 API 凭据。分析标的使用 `BTC/USDT:USDT` 这类永续格式；应用会为私有请求附加 OKX 模拟交易标识，启动时读取 Demo 余额、持仓并核验止损保护单。

启用 OKX 实盘时，在上述基础上改为 `CYP_MODE=live`、`CYP_OKX_DEMO=false`、`CYP_LIVE_ACK=1`，选择正确的 `CYP_OKX_REGION`，并使用真实账户凭据；同时必须配置 Bearer API 鉴权、风险告警、PostgreSQL 持久化、OKX 实时行情、永续与强制逐仓。账户必须是 net mode、非 portfolio margin，API key 只能有读取/交易权限且必须绑定 IP。上线前必须完成 [docs/GO_OPERATIONS.md](docs/GO_OPERATIONS.md) 的检查表（含 `scripts/regression.ps1` Demo 全链路回归与应急清仓演练）；部分成交会先撤销余量并确认终态，任何已成交数量都必须核实为同方向、同客户端标识、reduce-only 且全量覆盖的保护单，否则自动补挂，补挂耗尽自动紧急平仓并冻结新开仓。

所有密钥在配置快照和结构化日志中脱敏。`CYP_LLM_BASE_URL` 只能在启动时修改，HTTP 设置接口不能把已加载密钥重定向到其他主机。OKX key（Demo 或实盘）只授予读取和交易权限，必须禁用提现并配置 IP 白名单。

模型用量统一记录 `provider + model + Agent + symbol + source`。Anthropic、DeepSeek 和后续实现同一 Provider 接口的供应商会自动进入相同报表；供应商返回真实 token 时直接使用，否则明确标为“估算”。预算达到 70%/90% 会发出 SSE 告警，达到 100% 只拒绝后续模型调用，确定性分析、仓位监控、交易所原生保护单与自动平仓保持运行。

## Docker

以下命令构建 React 和 Go 服务，并启动 TimescaleDB/PostgreSQL 持久化：

```bash
export CYP_API_TOKEN="$(openssl rand -hex 32)"
docker compose up --build
```

PowerShell 可先执行 `$env:CYP_API_TOKEN = "<高强度随机值>"`。应用只映射到 `http://127.0.0.1:8000`，数据库映射到本机 `5433`；在 Dashboard 的设置页输入同一令牌后，写操作会自动携带 Bearer token。容器后端监听 `0.0.0.0`，未设置令牌时会 fail-closed 拒绝启动。

## 开发与 CI

```bash
gofmt -w cmd internal
go vet ./...
go test -race -count=1 ./...
go build ./cmd/cyp-server ./cmd/cyp

cd apps/web
npm ci
npm audit --audit-level=high
npx --yes @redocly/cli@2.38.0 lint ../../api/openapi.yaml
npm run typecheck
npm run build
```

CI 同时验证 workflow、Go 格式、vet、race test、两个二进制、OpenAPI、Web 依赖审计/类型检查/构建和容器构建。

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/KICKOFF.md](docs/KICKOFF.md) | 开发者入口、边界和完成标准 |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Go 分层架构与依赖方向 |
| [docs/AGENTS.md](docs/AGENTS.md) | Agent 职责、接口和降级路径 |
| [docs/RUNTIME.md](docs/RUNTIME.md) | 启动对账、扫描、监控与恢复 |
| [docs/RISK.md](docs/RISK.md) | 确定性风控规则 |
| [docs/QUANT.md](docs/QUANT.md) | 已实现量化内核与后续路线 |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Go 版本里程碑 |
| [docs/GO_OPERATIONS.md](docs/GO_OPERATIONS.md) | Go 服务运维手册 |
| [docs/GO_ROLLBACK.md](docs/GO_ROLLBACK.md) | 发布与归档分支回退 |

## 免责

本项目是研究和交易辅助工具，不构成投资建议。OKX Demo 操作会改变模拟账户中的订单和仓位；显式启用 OKX 实盘后会用真实资金下单，可能产生真实亏损。启用实盘前必须完成独立审计、Demo 灰度和小额资金验证，只投入可承受全损的资金。
