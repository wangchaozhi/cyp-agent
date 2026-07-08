# cyp-agent — 半自动加密货币多智能体交易助手

**分析 → 决策 → 风控 → 人工审批 → 执行 → 复盘** 的多智能体闭环。覆盖 **CEX 现货 / 合约永续 / 链上 DeFi**，统一 Venue 抽象。人在环中（human-in-the-loop）：Agent 只产出信号与订单意图，**任何真实下单前必须人工审批**。

核心设计哲学（沿用 game-asset-forge / prod-agent 三条铁律）：

1. **风控优先于智能**：确定性硬护栏（仓位/回撤/杠杆/授权额度）先于任何 LLM 决策，LLM 只能在护栏内建议，永不越权下单。
2. **无密钥可端到端跑通**：没有任何交易所 API Key、没有 LLM Key，也能用 `PaperVenue`（模拟盘）+ 规则模板信号跑完整闭环。
3. **契约单一来源**：`contracts/` 下的 pydantic 模型是前后端唯一真相，React 仪表盘的类型由它生成。

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
                                                   │ (确定性硬护栏)│◀───反馈闭环────│ 归因+经验沉淀 │
                                                   │ + 风控官(LLM) │               └──────────────┘
                                                   └──────────────┘
```

技术栈：**Python 核心**（ccxt · pandas · pandas-ta · pydantic · FastAPI · anthropic SDK · aiosqlite）+ **React/Vite 仪表盘**（REST + SSE 实时进度）。

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/KICKOFF.md](docs/KICKOFF.md) | **开工文档**：目标、范围、MVP 定义、里程碑、环境搭建、协作约定、Definition of Done |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 架构设计：分层、Venue 抽象、契约、降级矩阵、生产四大支柱 |
| [docs/AGENTS.md](docs/AGENTS.md) | 多智能体协作规格：每个 Agent 的职责/输入输出/降级/反馈闭环 |
| [docs/RUNTIME.md](docs/RUNTIME.md) | 运行时手册：触发/调度（三条循环）、止损落地（有仓必有保护）、对账恢复 |
| [docs/RISK.md](docs/RISK.md) | 风控手册：确定性硬护栏、CEX/合约/链上专项风险、Kill Switch |
| [docs/ROADMAP.md](docs/ROADMAP.md) | 迭代路线图：M0→M6 每一档的功能条目与验收标准 |

## 快速开始（M0 目标形态）

```bash
# 1. 安装（最小化：无需任何 Key 即可跑模拟盘）
pip install -e ".[dev]"

# 2. 复制配置，默认 paper 模式 + 规则信号（零密钥）
cp .env.example .env

# 3. 跑一个完整闭环（离线合成行情，零密钥）
python -m cyp.examples.run --symbol BTC/USDT --approve auto   # 自动批准（演示）
python -m cyp.examples.run --symbol BTC/USDT --approve cli    # 人工审批（半自动）
#   --data cex   切换为真实只读行情（无需密钥，需联网）
#   --venue okx  在 OKX Demo 模拟盘下单（需 OKX Demo 凭据 + 联网，零真实资金）

# 4. 启动仪表盘（浏览器打开 http://localhost:8000）
uvicorn apps.server.main:app --reload      # REST + SSE + 仪表盘同源直供
#   页面：事件流 / 待审批（点按钮人在环审批）/ 持仓 / Kill Switch
```

> ⚠️ **安全红线**：交易所 API Key 只授予「读 + 交易」权限，**永久禁用提现**；链上私钥走独立签名器（本地 keystore / KMS / 硬件），**永不落盘、永不进 LLM 上下文**。详见 [docs/RISK.md](docs/RISK.md)。

## 许可与免责

本项目为交易辅助工具，不构成投资建议。加密货币交易风险极高，实盘前请在 `paper` 模式充分验证，并自行承担全部风险。
