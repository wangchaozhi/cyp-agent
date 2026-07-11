# Go 重构完成记录

本文保留原文件名，内容改为重构完成后的实现记录和验收清单，不再作为双栈迁移计划。`main` 的后端、运行时、智能体、风控、回测与 CLI 已统一为 Go；主分支没有旧后端代理、shadow 双跑或运行时切换开关。

## 当前结论

- 服务入口：`cmd/cyp-server`；
- 命令行入口：`cmd/cyp`；
- Go module：`github.com/wangchaozhi/cyp-agent`；
- Go 版本：以 `go.mod` 的 `1.25.0` 为准；
- 前端：`apps/web` 的 React/Vite 应用；
- 默认运行：合成行情 + Mock/规则 LLM + PaperVenue + 原子 JSON 持久化；
- 可选持久化：memory、file、PostgreSQL；
- 当前执行边界：仅 Paper，CEX/链上真实下单硬禁用；
- 旧实现：仅冻结在 `archive/python-backend-20260710`，使用方式见 [`GO_ROLLBACK.md`](GO_ROLLBACK.md)。

主分支没有后端、执行器、风控或智能体的旧实现兼容开关；不应重新引入双栈选择器。

## 已完成范围

### 工程与入口

- 建立 `go.mod`/`go.sum` 和标准 `cmd`、`internal` 包边界；
- `cyp-server` 提供 REST、SSE、静态前端托管、信号处理与优雅关闭；
- `cyp` 提供 backtest、sweep、config、version；
- 版本通过 `VERSION`、Go linker flag 和前端 package version 对齐；
- Docker 使用 Node 构建前端、Go 构建静态二进制，最终镜像不携带旧运行时。

### 契约与配置

- 核心 DTO、枚举和事件迁至 `internal/contracts`；
- 实现精确 Decimal，JSON 金额统一输出十进制字符串；
- `internal/config` 支持默认值、`.env`、环境变量优先级和严格校验；
- 配置快照、日志和持久化检查点均做密钥脱敏；
- REST/SSE 外部契约落在 `api/openapi.yaml` 与 `api/jsonschema/`。

### API 与前端契约

- 健康、就绪、场所、设置、行情、持仓、组合、风控、指标、pending、回测、run、审批、Kill Switch 和事件流均由 Go handler 提供；
- `/api/run` 异步返回 `run_id`，可通过 run 状态、REST 快照和 SSE 观察；
- 错误体保持 `{"detail":"..."}`，请求体限制为单个 JSON 值；
- API Key 和 DSN 不出现在 settings 响应；
- Go 服务可直接托管 `apps/web/dist`，开发时由 Vite 代理 `/api`。

### 智能体与 LLM

- 技术、衍生品、情绪和链上分析师并行执行并保持确定顺序；
- 单个分析维度失败局部降级，不泄露上游敏感错误；
- Strategist、RiskOfficer、Reviewer 接入同一编排流程；
- 支持 Anthropic、DeepSeek 和无密钥 Mock；
- LLM 客户端包含超时、瞬态重试、熔断以及调用数/token/成本/墙钟预算。

### 风控、审批与执行

- 确定性风控覆盖仓位、敞口、集中度、相关敞口、滑点、杠杆、保证金和回撤等限制；
- 人工审批支持 approve、reject、modify 和 fail-safe 超时；
- 修改规模后必须重新通过确定性风控；
- auto 审批只有在 symbol 白名单、风险分数和 quote 上限全部满足时生效；
- Kill Switch、SafetyState、运行模式和 Venue 在执行前分层检查；
- PaperVenue 支持预检、幂等下单、持仓、余额、保护单和关闭仓位；
- Binance/OKX 实现公共行情、签名请求与错误分类，但交易操作硬禁用；
- 链上适配器包含白名单、MEV、gas/价格冲击预检与隔离 signer 边界，未接入默认执行路径。

### 数据、运行时与恢复

- 默认合成行情可无密钥运行；
- CEX 源可读取 ticker、OHLCV、order book 等公共数据；
- MarketAggregator 提供跨场所行情与资金费摘要；
- 每次启动都先对 Paper 持仓和保护单执行 reconcile；
- 只有成功 reconcile 可以解除新仓冻结；
- 可选 Scanner 与 PositionMonitor 常驻循环，支持 symbol 锁、并发限制和告警；
- 关闭时通过 context 取消所有后台 goroutine 和等待者。

### 持久化与回测

- `persistence.Repository` 统一检查点和 lessons 边界；
- memory 实现用于测试；
- file 实现采用原子替换、权限限制与中断恢复；
- PostgreSQL 实现使用 pgx 连接池，支持 schema 初始化、upsert 和有界 lessons；
- 编排器保存 proposal、risk、approval、order intent、execution 和 result；
- 回测、扫参、walk-forward、purged split、PSR/DSR/PBO、稳健性评分和 PostgreSQL 归档均已迁入 `internal/backtest`。

### 工程化

- Go 单元、契约、API、运行时、持久化、Venue、LLM、风控和回测测试位于各包同目录；
- CI 检查 gofmt、go vet、race tests、两个 Go 命令构建、前端 typecheck/build 和 Docker build；
- Release workflow 校验 tag、`VERSION` 和前端版本，构建多平台 Go 压缩包、前端产物和 SHA-256 校验和；
- `scripts/start-dev.ps1` 只启动 Go 与 Vite，不依赖旧环境。

## 当前包映射

| 领域 | Go 实现 |
| --- | --- |
| 服务装配 | `internal/app` |
| HTTP/SSE | `internal/api` |
| 数据契约 | `internal/contracts` |
| 配置与安全门禁 | `internal/config`、`internal/control` |
| 工作流编排 | `internal/orchestrator` |
| 多智能体 | `internal/agents` |
| LLM provider | `internal/llm` |
| 硬风控 | `internal/risk` |
| 审批 | `internal/approval` |
| 场所与撮合 | `internal/venue` |
| 行情与指标 | `internal/data` |
| 运行时与对账 | `internal/runtime` |
| 持久化 | `internal/persistence` |
| 组合视图 | `internal/portfolio` |
| 回测与验证 | `internal/backtest` |
| 事件、指标、日志 | `internal/events`、`internal/metrics`、`internal/observability` |
| 告警 | `internal/alerts` |

## 验收命令

在仓库根目录执行。任何一步失败都不应 push 或打 tag。

```powershell
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8

$unformatted = gofmt -l cmd internal
if ($unformatted) { $unformatted; throw "Go files need gofmt" }

go mod tidy
git diff --check -- go.mod go.sum
go vet ./...
go test -count=1 ./...
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -o ./bin/cyp-server.exe ./cmd/cyp-server
go build -trimpath -o ./bin/cyp.exe ./cmd/cyp

Push-Location apps/web
npm ci
npm audit --audit-level=high
npm run typecheck
npm run build
Pop-Location

docker build -t cyp-agent:verify .
```

确认主分支没有旧源文件或依赖清单：

```powershell
$OldSources = rg --files -g '*.py' -g 'pyproject.toml' -g 'environment.yml'
if ($OldSources) { $OldSources; throw "发现未移除的旧后端文件" }
```

执行服务 smoke test 时至少验证：

1. `/api/health` 返回 `ok=true`；
2. `/api/ready` 返回 `ready=true` 且 `safety.frozen=false`；
3. `/api/settings` 不包含原始 Key 或 DSN；
4. `/api/run` 返回 `run_id`；
5. 人工 approve/reject/modify 与超时路径正确；
6. Paper 成交后 positions、portfolio、metrics 和 SSE 更新；
7. Kill Switch 阻止新仓但允许关闭已有 Paper 仓位；
8. `mode=live` 或非 Paper execution venue 无法执行；
9. CEX `Place`/`Cancel` 无论凭据如何均拒绝；
10. 重启后 file/PostgreSQL 检查点与 lessons 可读取。

## 发布门禁

发布版本必须同时满足：

- 工作树只包含本次重构相关变更；
- 上述 Go、前端和容器验证通过；
- CI workflow 已切换为纯 Go 后端检查；
- `VERSION`、`apps/web/package.json` 与 tag 去掉 `v` 后一致；
- `api/openapi.yaml`、JSON Schema、README 和运维文档与实现一致；
- 归档分支已推送且冻结提交可验证；
- main push 成功后再创建并推送 annotated tag；
- 禁止 force-push main 或移动已发布 tag。

## 有意保留的安全限制

以下项目不是重构遗漏，而是当前版本明确的安全边界：

- 不支持真实 CEX 或链上下单；
- 不支持通过配置解除 live 硬门禁；
- 不提供主分支到旧后端的运行时回退；
- SSE 总线不做持久事件回放；
- 文件 Repository 只支持单实例；
- 写 API 已支持 `CYP_API_TOKEN`，非回环监听强制启用；公网仍需 TLS、访问控制和审计；
- 多实例租约、持久订单状态机和远端真实账户 reconcile 尚未达到实盘门禁要求。

若未来要增加实盘能力，应作为新的、独立审计的安全项目推进：先实现持久订单状态机、远端对账、未知订单恢复、原生保护单、细粒度授权和 testnet/demo 故障演练，再讨论受控启用。不得通过删除当前 guard 或把常量改为 `true` 的方式上线。

## 维护规则

- 新后端功能直接实现为 Go package，不引入旁路服务；
- handler 只做协议适配，业务状态转换留在领域服务；
- 风控、审批、对账、执行的顺序不能由单个 API 参数跳过；
- 外部契约变更必须同步 OpenAPI、JSON Schema、前端类型和测试；
- Repository、Venue、Data Source、LLM provider 通过既有接口扩展；
- 所有后台任务必须有 owner、context 取消和可测试的停止路径；
- 历史归档保持不可变，只通过独立 worktree 访问。
