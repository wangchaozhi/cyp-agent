# cyp-agent 路线图

路线图以 `main` 的 Go 实现为准。完成标记表示代码、测试和文档已落地，不代表已授权真实资金执行。

## 版本状态

| 版本/阶段 | 状态 | 结果 |
| --- | --- | --- |
| v0.2.0 · Go 全量重构 | ✅ 完成 | Go 后端 + React，原实现仅保存在归档分支 |
| G0 · 安全 Paper 闭环 | ✅ 完成 | 零密钥采集、Agent、硬风控、审批、模拟执行、复盘 |
| G1 · 运行时与恢复 | ✅ 完成 | 启动对账、冻结门、扫描、监控、检查点、优雅停机 |
| G2 · 数据与回测 | ✅ 完成 | 合成/CEX 只读行情、回测、扫参、OOS/PBO/DSR、OHLCV 归档 |
| G3 · 可运维服务 | ✅ 完成 | REST/SSE、React、指标、Docker、PostgreSQL、CI/Release |
| G4 · 实盘状态机 | ⏳ 未开始 | 持久订单状态、真实场所对账、故障注入、测试网验收 |
| G5 · 链上执行 | ⏳ 未开始 | RPC 数据、隔离签名、模拟网执行、授权/MEV 全链路验证 |

## G0：安全 Paper 闭环

- [x] `internal/contracts` 统一领域与 wire 契约，资金字段使用精确 Decimal。
- [x] 技术面、衍生品、情绪、链上四个分析师并行且失败隔离。
- [x] 策略官根据报告、波动和风险预算生成结构化提案。
- [x] 确定性风控覆盖止损、单笔风险、仓位、总敞口、集中度、相关簇、杠杆、爆仓缓冲、保证金、滑点、CVaR、Kill/对账门。
- [x] 风控官只能收紧硬风控结果；无 LLM 时稳定降级。
- [x] Dashboard 审批和受约束的自动审批；修改后重新校验。
- [x] `PaperVenue` 幂等撮合、保护单、持仓和平仓。
- [x] 复盘经验写入仓储并注入后续轮次。

验收：默认配置不依赖密钥和网络，API 可完成 run → pending → approve/reject → result；所有执行均为 Paper。

## G1：运行时与恢复

- [x] 每次启动先执行只读对账；失败时 `SafetyState` 保持冻结。
- [x] watchlist 扫描循环，按 symbol 去重并隔离单轮错误。
- [x] 持仓监控覆盖保护单缺口、止损逼近、爆仓价逼近、异常波动和保证金率。
- [x] Kill Switch 拒绝新仓，平仓路径独立保留。
- [x] memory/file/PostgreSQL 三种 Repository。
- [x] 检查点、经验检索、SSE 历史和运行指标。
- [x] 服务信号处理、长连接唤醒和有界优雅停机。

后续增强：进程间 symbol 锁、多实例 leader election、分布式事件总线、持仓快照与外部账本差异自动修复。

## G2：数据与量化验证

- [x] 确定性合成行情，无外部服务即可复现。
- [x] Binance/OKX 原生 Go HTTP 公共行情、资金费和历史 K 线读取。
- [x] 跨所 ticker/funding 聚合与套利线索，任何单所失败可降级。
- [x] 回测报告：总收益、最大回撤、Sharpe、胜率、盈亏因子和交易明细。
- [x] 参数网格、默认目标函数、样本外评估和稳健结论。
- [x] Walk-forward、purged K-fold/embargo、PBO、PSR、Deflated Sharpe、MinTRL。
- [x] EWMA/已实现波动率，以及策略官的波动率止损/仓位入口。
- [x] PostgreSQL OHLCV 增量归档。

下一批：

- [ ] 手续费、点差、资金费和非线性冲击成本模型。
- [ ] Block/bootstrap 交易序列，输出收益和回撤置信区间。
- [ ] 使用真实收益协方差替换静态相关簇。
- [ ] 多资产 HRP/ERC 与约束优化。
- [ ] Regime 分层、参数稳定性曲面和数据质量报告。

## G3：服务、前端与交付

- [x] Go `net/http` REST、SSE、React 静态资源托管。
- [x] 健康、就绪、场所、设置、市场、运行、审批、持仓、风险、组合、回测、指标和 Kill Switch API。
- [x] React 仪表盘覆盖信号流、审批、仓位、风险、市场、设置和回测。
- [x] OpenAPI 与 dashboard event JSON Schema。
- [x] 多阶段 Docker 镜像；Compose 启动应用和 TimescaleDB。
- [x] CI 验证 gofmt、vet、race test、二进制、Web 与容器。
- [x] tag release 交叉构建二进制、Web 包和校验和。

后续增强：OpenAPI 自动生成前端类型、Prometheus exporter、OpenTelemetry exporter、数据库备份恢复演练和容器镜像签名。

## G4：真实场所状态机（解除硬门前的强制清单）

当前 `config.LiveExecutionSupported` 为 `false`。以下项目全部完成、独立审计并通过验收后，才可讨论修改：

- [ ] 持久化 OrderIntent/Order/Ack/Fill/Cancel 状态机，所有转移可幂等重放。
- [ ] Binance 与 OKX 的远端订单、成交、余额、仓位和保护单对账。
- [ ] 下单超时后的未知状态处理，禁止盲目重试。
- [ ] 原生止损/止盈创建失败后的确定性补救与人工告警。
- [ ] API 限频、时钟偏差、签名错误、部分成交、断网和进程崩溃故障注入。
- [ ] Testnet/Demo 全链路回归与自动清仓脚本。
- [ ] 最小权限、IP 白名单、密钥轮换和审计日志验证。
- [ ] 双人审批的发布开关与小额灰度回滚手册。

验收必须证明：任何不确定状态都会冻结新仓；重启和重放不会重复成交；保护单缺失可被立即发现；Kill Switch 始终可用。

## G5：链上执行

`internal/venue/onchain.go` 和签名器目前只提供安全抽象与离线测试，不在应用执行链中。后续顺序：

1. 只读 RPC/索引数据和合约白名单。
2. 本地开发链与测试网 preflight、精确授权、nonce、确认与 revert 恢复。
3. KMS/硬件签名器，私钥不进入主进程和 Agent 上下文。
4. 流动性、价格冲击、gas、MEV 私有路由和合约风险硬护栏。
5. 小额测试网验收、审计和主网灰度；默认仍需人工审批。

永久不做：自动提现/转账、无限授权、明文私钥、未经审计的任意合约调用。

## 量化子路线

| 阶段 | 目标 | 状态 |
| --- | --- | --- |
| Q1 | 时序验证、PBO、PSR/DSR、EWMA、CVaR 门 | ✅ 核心已完成 |
| Q2 | 真实成本、bootstrap、实测协方差、HRP/ERC | ⏳ 计划中 |
| Q3 | Regime、协整/状态空间、执行优化 | ⏳ 研究阶段 |
| Q4 | 标签工程、可解释模型和严格模型治理 | ⏳ 研究阶段 |

数学定义、默认阈值和测试要求见 [QUANT.md](QUANT.md) 与 [quant/README.md](quant/README.md)。
