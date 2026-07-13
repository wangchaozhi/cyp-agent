# 更新日志

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/)，版本遵循语义化版本。

## [未发布]

暂无。

## [1.0.0] - 2026-07-13

### 运行模式与 OKX Demo

- 使用 Mode Policy 策略和 Venue 执行身份分离本地 Paper、OKX Demo 与 Live 权限边界。
- 风险 checkpoint 按账户环境隔离；旧 Paper 本金和回撤不会再污染 OKX Demo。
- OKX Demo 支持真实模拟盘下单、持仓、减仓以及 conditional/OCO 原生止损止盈核验。
- 对账异常时保持 API 与减仓能力可用，同时冻结新仓，避免有持仓时整个服务离线。

### 仪表盘与分析

- 重设计仪表盘布局、顶部运行模式与分析币种控件，修复下拉交互和点击焦点样式。
- 新增行情曲线、多币种选择和分析币种配置页；非敏感 watchlist 可持久化，重启后继续生效。
- 运行时间线明确区分名义金额、参考价与止损价，避免金额被误读为币价。
- 版本统一为 `1.0.0`。

## [0.2.0] - 2026-07-11

### 重大变更 · 后端全量 Go 重构

- 当前 `main` 后端全部改为 Go 1.25，提供 `cyp-server` REST/SSE 服务和 `cyp` 运维/回测 CLI。
- 原后端历史快照保存在 `archive/python-backend-20260710` 分支；`main` 不再包含其源码、项目配置或运行入口。
- React 18 + TypeScript + Vite 8 仪表盘继续使用稳定的 REST/SSE 契约，由 Go 服务托管构建产物。
- 版本统一为 `0.2.0`，发布流程通过 ldflags 注入两个 Go 二进制。

### Go 领域与应用

- `internal/contracts` 提供领域/API 契约和精确 Decimal；资金字段通过 JSON 十进制字符串传输。
- `internal/config` 支持 `.env`、环境变量覆盖、严格校验和脱敏快照。
- 技术面、衍生品、情绪、链上分析师并行运行；策略官、风控官和复盘官接入完整 Orchestrator 闭环。
- LLM 层支持 Anthropic 与 DeepSeek，含独立 session 预算、超时、重试、熔断、结构化输出和规则降级。
- 确定性风控覆盖止损、单笔风险、仓位、敞口、相关簇、杠杆、爆仓缓冲、保证金、滑点、链上预检、CVaR、Kill Switch 和对账冻结。
- 审批支持 Dashboard 的批准/拒绝/修改和受白名单、风险分、金额共同限制的自动策略；修改后强制重新风控。

### 场所、数据与运行时

- `PaperVenue` 支持现货/永续模拟撮合、幂等订单、保护单、持仓与平仓。
- Binance/OKX 使用原生 Go HTTP 客户端提供公共行情、历史 K 线、私有读取和签名能力，保持只读。
- 链上场所与隔离签名器保留安全接口和离线测试，尚未接入应用执行链。
- 新增跨所 ticker/funding 聚合、确定性合成行情、指标与波动率计算。
- Runtime 在每次启动时先做 Paper 对账；成功前 SafetyState 冻结新仓。可选启动 watchlist 扫描、持仓监控、Webhook 告警和运行指标。
- 新增 memory、原子 JSON file、PostgreSQL 三种 Repository；检查点写入前递归屏蔽敏感字段。

### 回测与量化

- 新增确定性合成回测和历史蜡烛回放，输出收益、最大回撤、Sharpe、胜率、盈亏因子、交易和权益曲线。
- 新增参数网格、目标函数排序、样本外评估与稳健结论。
- 新增 walk-forward、purged K-fold/embargo、PBO、Probabilistic Sharpe、Deflated Sharpe 和 MinTRL。
- 新增 EWMA/realized volatility，以及 PostgreSQL OHLCV 增量归档。
- 明确当前基础回测尚未包含完整手续费、点差、资金费和冲击成本，不能作为上线依据。

### API、前端与交付

- Go API 覆盖 health/ready、venues、settings、market、run、events、pending/approvals、positions、risk、portfolio、backtest、metrics 和 Kill Switch。
- 更新 OpenAPI、Dashboard event JSON Schema、React API 类型及开发代理配置。
- Windows 开发脚本改为直接启动 Go 服务和 Vite，不依赖额外运行环境。
- Docker 使用 Node/Go 多阶段构建，运行镜像只包含静态资源和 Go 二进制；Compose 可启动应用与 TimescaleDB。
- CI 更新为 workflow lint、gofmt、vet、race test、Go 构建、OpenAPI lint、Web 依赖审计/类型检查/构建和容器构建。
- Release workflow 在 `v*` tag 上交叉构建 Go 二进制、打包 Web 资源、生成校验和并创建 GitHub Release。
- 全面更新 README、架构、运行时、Agent、量化、运维、回退和路线图文档。

### 安全

- `config.LiveExecutionSupported=false` 在编译期硬禁真实下单；API key 与 `CYP_LIVE_ACK=1` 都不能解除。
- 非 `paper` mode/venue、Kill Switch、对账冻结和未解决保护单缺口均拒绝新仓。
- Agent 包不依赖场所或审批包，LLM 无法直接调用执行能力。
- 配置、日志、LLM 上下文和检查点对 API key、token、私钥及 DSN 做脱敏。
- 非回环监听强制配置 `CYP_API_TOKEN`；写请求经过 Bearer token、同源与 JSON Content-Type 校验。
- LLM Base URL 改为启动期配置，运行中的 HTTP 请求不能把已加载密钥重定向到其他主机。

## [0.1.x] - 历史版本

- 建立多智能体交易助手的最初闭环、风控规则、CEX/链上实验、回测研究和 React 仪表盘。
- 该系列的最终源码状态已固定在 `archive/python-backend-20260710`，仅用于审计和历史参考；当前 `main` 不使用该实现。
