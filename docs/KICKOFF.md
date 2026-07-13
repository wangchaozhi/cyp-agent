# cyp-agent 开发者入口

本文是当前 Go 代码库的开工说明。架构细节见 [ARCHITECTURE.md](ARCHITECTURE.md)，Agent 约定见 [AGENTS.md](AGENTS.md)，运行时安全门见 [RUNTIME.md](RUNTIME.md)，确定性护栏见 [RISK.md](RISK.md)。

## 1. 产品定义

cyp-agent 是半自动加密资产交易助手：系统采集市场快照，由多个只读 Agent 并行分析，策略官生成提案，确定性风控和风控官评审后交给人工审批，最终在本地 Paper 或显式配置的 OKX Demo 中模拟执行并复盘。

设计不变量：

1. 硬风控先于审批和执行，LLM 无权放宽硬护栏。
2. 审批修改后的提案必须重新通过确定性风控。
3. 单个分析师失败只降级该维度，不得破坏其他报告。
4. 无 LLM key、无交易所 key、无数据库时仍可跑通闭环。
5. 编译期硬禁真实下单；非 Paper 场所只允许显式 OKX Demo USDT 永续执行。
6. Kill Switch 和对账冻结拒绝新仓，但不能阻断安全平仓。

## 2. 当前范围

| 范围 | 当前实现 |
| --- | --- |
| 市场 | Paper 现货/永续；Binance/OKX 公共行情与历史 K 线只读 |
| Agent | 技术面、衍生品、情绪、链上分析师；策略官、风控官、复盘官 |
| 风控 | 止损、风险预算、仓位、敞口、相关簇、杠杆、爆仓缓冲、滑点、CVaR、Kill/对账等 |
| 审批 | Dashboard 人工审批；白名单 + 风险分 + 金额上限的 `auto` 策略 |
| 执行 | Paper 撮合；OKX Demo USDT 永续、幂等订单、原生保护单、持仓与平仓 |
| 服务 | Go REST/SSE、React 静态资源、健康/就绪、指标和优雅停机 |
| 持久化 | memory、原子 JSON file、PostgreSQL |
| 回测 | 合成/历史蜡烛回放、扫参、OOS/PBO/DSR 和统计验证 |

当前不包含真实资金执行、提现/转账、托管资金、高频交易和主网链上 swap。链上场所与签名器代码是安全边界骨架，尚未接入应用执行链。

## 3. 技术栈

- Go 1.25，标准库 HTTP 服务，`pgx/v5` PostgreSQL 驱动。
- React 18、TypeScript、Vite 8（Node.js 20.19+ 或 22.12+）；前端契约在 `apps/web/src/shared/api/types.ts`。
- PostgreSQL/TimescaleDB 为可选持久化；默认使用原子 JSON 文件。
- Anthropic 原生接口或 DeepSeek OpenAI-compatible 接口；缺少 key 自动走规则路径。

## 4. 目录与依赖方向

```text
cmd/cyp-server       组合应用并启动 HTTP 服务
cmd/cyp              运维/回测 CLI
        │
internal/app         依赖注入和资源生命周期
        │
internal/api         REST / SSE / 静态资源
internal/orchestrator闭环状态机
        │
├─ agents            只读分析、提案和复盘
├─ risk              确定性硬护栏
├─ approval          pending 队列与审批决策
├─ venue             Paper/CEX/Onchain 抽象
├─ data              行情、指标与跨所聚合
├─ runtime           对账、扫描、监控
├─ persistence       检查点和经验仓储
└─ contracts         领域与 wire 契约、精确 Decimal
```

领域包不能依赖 `cmd` 或 `internal/app`。`internal/agents` 不导入 `venue` 和 `approval`，因此 Agent 本身拿不到下单能力。金额、价格和数量必须使用 `contracts.Decimal`，JSON 对外统一为十进制字符串。

## 5. 一轮闭环

```text
MarketSnapshot
  → 四个 Analyst 并行报告
  → Strategist 生成 TradeProposal
  → Venue.Preflight
  → risk.Assess 硬风控
  → RiskOfficer 只能收紧
  → Dashboard/auto ApprovalDecision
  → 修改项重新 risk.Assess
  → PaperVenue.Place
  → Reviewer + lessons 持久化
```

每个阶段发 SSE 事件并写必要检查点。启动服务时，无论是否开启长期 Runtime，都先进行只读对账；对账失败保持冻结状态。

## 6. 本地启动

前置要求：Go 1.25+；开发 Web 需要 Node.js 20.19+ 或 22.12+。

```powershell
Copy-Item .env.example .env
.\start-dev.bat
```

也可只启后端：

```powershell
go run ./cmd/cyp-server -host 127.0.0.1 -port 8000
```

默认审批模式即 `dashboard`（Web/API 审批，`cli` 为废弃别名）。默认 `CYP_PERSISTENCE=file`，无需数据库；需要 PostgreSQL 时：

```powershell
docker compose up -d db
$env:CYP_PERSISTENCE = "postgres"
go run ./cmd/cyp-server
```

## 7. 开发流程

### 契约变更

1. 先修改 `internal/contracts` 和 `api/openapi.yaml`。
2. 同步 `apps/web/src/shared/api/types.ts`。
3. 为十进制字符串、可选字段和错误响应补前后端测试。
4. 不在 API handler 中复制领域模型或使用 `float64` 表示资金。

### 新增 Agent

1. 在 `internal/agents` 实现小而明确的接口。
2. 将外部依赖放入 `AgentContext`，不要读取全局状态。
3. 失败时产出 `degraded=true` 的结构化报告。
4. 注册到 `AllAnalysts()`，并测试成功、缺数据和取消路径。

### 新增风控规则

1. 在 `internal/risk/engine.go` 写确定性函数。
2. 明确规则只拒绝新仓还是也作用于退出路径。
3. 添加正常、边界、拒绝、缩量测试。
4. LLM 软评审只能提高风险分或拒绝，不得复活硬拒绝。

### 新增场所

1. 实现 `venue.Venue` 和最小 `Caps`。
2. 默认只读，显式区分公共行情、私有读取、预检和下单能力。
3. 在 `VenueRegistry` 注册并写 HTTP/签名/错误映射测试。
4. 在实盘状态机、持久订单、启动对账和失败恢复全部完成前，不接通 `Place`。

## 8. 提交前检查

```powershell
$files = gofmt -l cmd internal
if ($files) { $files; throw "Go files need gofmt" }
go vet ./...
go test -race -count=1 ./...
go build ./cmd/cyp-server ./cmd/cyp

Push-Location apps/web
npm ci
npm audit --audit-level=high
npm run typecheck
npm run build
Pop-Location
```

高风险变更还需要：

- 风控/Decimal/幂等/审批修改：必须有边界与失败路径测试。
- API 契约：运行现有 API 集成测试并手工验证一次 SSE/审批闭环。
- 持久化：分别验证 file 与 PostgreSQL 的启动恢复。
- 容器或发布：`docker build .`，并核对 `VERSION` 与 Web 包版本一致。

## 9. Definition of Done

- Go 格式、vet、race test、构建和 Web 类型检查全部通过。
- 默认配置零密钥、零数据库可启动并完成 Paper 闭环。
- 新增失败模式有明确的 fail-closed 行为和可观测错误。
- 日志、事件、配置快照不泄漏密钥、DSN 密码或私钥。
- 文档、OpenAPI、环境变量示例和实现保持一致。
- 未经完整实盘安全里程碑，不得解除 `config.LiveExecutionSupported=false`。
