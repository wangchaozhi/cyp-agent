# cyp-agent

半自动加密货币多智能体交易助手。系统按 **采集 -> 分析 -> 决策 -> 风控 -> 人工审批 -> 执行 -> 复盘** 串起闭环，覆盖 CEX 现货、永续合约和后续链上 DeFi 的统一 Venue 抽象。

核心原则很简单：**风控先于智能，模拟先于真钱，人在环先于自动化**。Agent 可以生成信号和订单意图，但任何真实下单都必须先通过确定性风控与人工审批。

## 当前状态

| 方向 | 状态 |
| --- | --- |
| M0 骨架闭环 | 已完成：无密钥离线跑通 7 步闭环，含 CLI、审批门、FastAPI、SSE、仪表盘、Kill Switch |
| M1 永续合约 | 已完成模拟盘侧保证金、杠杆、爆仓缓冲、逐仓与维持保证金护栏 |
| M2 CEX 实盘 | 离线实现完成：ccxt 下单、保护单、LiveGuard、熔断、告警；真实网络 Testnet/小额实操仍需凭据验证 |
| M4 多所与组合 | 部分完成：Binance/OKX 适配、OKX Demo、跨所行情聚合、组合级风控与组合面板 |
| M5 回测与择优 | 部分完成：合成历史回放、同管线回测、扫参、样本外验证与 PBO/DSR 基础件 |
| Q1 量化内核 | 部分完成：walk-forward、purged K-fold、PBO、PSR/DSR/MinTRL、EWMA 波动率、VaR/CVaR 护栏 |

详见 [CHANGELOG.md](CHANGELOG.md) 与 [docs/ROADMAP.md](docs/ROADMAP.md)。

## 架构一览

```
        数据/情报层                并行分析师团            决策 & 风控           人在环          执行 & 复盘
┌──────────────────────┐   ┌──────────────────┐   ┌──────────────┐   ┌────────┐   ┌──────────────┐
│ 行情/K线/订单簿(ccxt) │   │ 技术面分析师      │   │ 首席策略官    │   │ 审批门 │   │ 交易员        │
│ 资金费/持仓/爆仓价    │──▶│ 衍生品分析师      │──▶│ 合成          │──▶│ 人工    │──▶│ 幂等下单      │
│ 链上流向/聪明钱/DEX   │   │ 情绪分析师        │   │ TradeProposal │   │ 批准/  │   │ 订单生命周期  │
│ 社媒/新闻/宏观        │   │ 链上分析师        │   └──────┬───────┘   │ 拒绝/  │   └──────┬───────┘
└──────────────────────┘   └──────────────────┘          │           │ 修改   │          │
                                                   ┌──────▼───────┐   └────────┘   ┌──────▼───────┐
                                                   │ 风控引擎      │ 否决→重议      │ 复盘官        │
                                                   │ 硬护栏        │◀──反馈闭环────│ 归因+经验沉淀 │
                                                   │ + 风控官      │               └──────────────┘
                                                   └──────────────┘
```

技术栈：

- Python 核心：ccxt、pandas、pydantic、FastAPI、aiosqlite、可选 Anthropic SDK
- 前端仪表盘：React、Vite、TypeScript、REST + SSE
- 契约单一来源：`core/cyp/contracts/` 下的 pydantic 模型

## 快速开始

要求：Python 3.10+，如需开发仪表盘还需要 Node.js 18+。

```bash
# 安装 Python 包与开发依赖
pip install -e ".[dev]"

# 复制配置；默认 paper 模式、mock/rule LLM、零密钥可跑
cp .env.example .env

# Windows PowerShell 可用：
# Copy-Item .env.example .env
```

跑一轮离线模拟闭环：

```bash
python -m cyp.cli --symbol BTC/USDT --approve auto

# 等价入口
cyp --symbol BTC/USDT --approve cli
python -m cyp.examples.run --symbol BTC/USDT --approve auto
```

常用参数：

```bash
# 使用真实 CEX 只读行情；无需交易所 key，但需要联网
python -m cyp.cli --data cex --symbol BTC/USDT --approve cli

# 启用运行时扫描循环，包含启动对账与持仓监控
python -m cyp.cli --loop 3 --approve auto

# OKX Demo 模拟交易；需要 OKX Demo API key/secret/passphrase
python -m cyp.cli --venue okx --data cex --approve cli
```

## 服务与仪表盘

后端服务：

```bash
uvicorn apps.server.main:app --reload
```

常用端点：

| 端点 | 说明 |
| --- | --- |
| `GET /api/health` | 健康检查、运行模式、LLM 状态、Kill Switch 状态 |
| `POST /api/run` | 触发一轮闭环 |
| `GET /api/events` | SSE 事件流 |
| `GET /api/pending` | 待人工审批列表 |
| `POST /api/approvals/{run_id}` | 批准、拒绝或修改订单意图 |
| `GET /api/positions` | 当前持仓 |
| `GET /api/risk` | 风控看板数据 |
| `GET /api/portfolio` | 跨场所组合视图 |
| `GET /api/market` | 跨所报价与最优买卖场所 |
| `GET/POST /api/killswitch` | 查询或切换 Kill Switch |

React 仪表盘开发：

```bash
cd apps/web
npm ci
npm run dev
```

同源部署到 FastAPI：

```bash
cd apps/web
npm ci
npm run build

cd ../..
uvicorn apps.server.main:app --reload
```

构建后访问 `http://localhost:8000/`。未构建时，FastAPI 会显示一个简易占位页。

## 回测与策略择优

```bash
# 单次回测：合成历史，复用同一套 Orchestrator 管线
python -m cyp.backtest.run --symbol BTC/USDT --bars 300 --drift 0.001

# 批量扫参：按默认目标函数排序，并输出样本外验证、PBO、Deflated Sharpe
python -m cyp.backtest.sweep --symbol BTC/USDT --bars 300 --top 5
```

当前回测数据源以可复现的合成历史为主，真实历史归档接入仍在后续迭代中。

## 配置

复制 `.env.example` 为 `.env` 后即可运行。默认值是保守的 paper 模式，缺少 API key 时会自动降级。

关键环境变量：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `CYP_MODE` | `paper` | `paper` 或 `live`；实盘还需通过 LiveGuard |
| `CYP_APPROVAL` | `cli` | `cli`、`dashboard` 或 `auto` |
| `CYP_KILL` | `0` | `1` 时拒绝新提案与下单 |
| `CYP_ALLOW_PERP` | `0` | 是否允许策略官提出永续合约 |
| `ANTHROPIC_API_KEY` | 空 | 留空时 LLM 走 mock/rule 降级 |
| `CYP_CEX_ID` | `binance` | 默认 CEX |
| `CYP_LIVE_ACK` | `0` | `mode=live` 时必须显式设为 `1` |
| `OKX_*` | 空 | OKX Demo 凭据，`OKX_PASSWORD` 是 passphrase |
| `CYP_ALERT_WEBHOOK` | 空 | 可选告警 webhook |
| `CYP_MAX_RISK_PER_TRADE` | `0.01` | 单笔风险上限 |
| `CYP_MAX_CVAR_PCT` | `0.03` | 组合 CVaR 尾部损失上限 |
| `CYP_DB_PATH` | `./data/cyp.db` | 运行时检查点与状态存储 |

完整配置见 [.env.example](.env.example)，风控阈值解释见 [docs/RISK.md](docs/RISK.md)。

## 开发命令

```bash
# Python 测试
python -m pytest

# Python lint
python -m ruff check .

# Web 类型检查与构建
cd apps/web
npm ci
npm run typecheck
npm run build
```

## 文档索引

| 文档 | 内容 |
| --- | --- |
| [docs/KICKOFF.md](docs/KICKOFF.md) | 目标、范围、MVP、环境搭建、协作约定、Definition of Done |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 分层架构、Venue 抽象、契约、降级矩阵、生产支柱 |
| [docs/AGENTS.md](docs/AGENTS.md) | 多智能体职责、输入输出、降级路径、反馈闭环 |
| [docs/RUNTIME.md](docs/RUNTIME.md) | 调度循环、启动对账、止损保护、崩溃恢复 |
| [docs/RISK.md](docs/RISK.md) | 风控硬护栏、CEX/合约/链上专项风险、Kill Switch |
| [docs/QUANT.md](docs/QUANT.md) | 数学金融内核升级路线 |
| [docs/quant/README.md](docs/quant/README.md) | 量化模型规格分册入口 |
| [docs/ROADMAP.md](docs/ROADMAP.md) | M0-M6 与 Q1-Q4 路线图 |

## 安全红线

- 交易所 API key 只授予「读 + 交易」权限，永久禁用提现，并启用 IP 白名单。
- `live` 模式必须满足 key 存在、`CYP_LIVE_ACK=1`、Kill Switch 未开启，否则系统退回只读。
- 私钥必须走独立签名器或加密 keystore，永不明文落盘、永不进入日志、永不进入 LLM 上下文。
- 任意实盘前先用 `paper`、OKX Demo 或交易所 Testnet 跑通，并从小额灰度开始。

## 许可与免责

本项目为交易辅助工具，不构成投资建议。加密货币交易风险极高，实盘前请在模拟环境充分验证，并自行承担全部风险。
