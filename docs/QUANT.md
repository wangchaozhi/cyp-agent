# 量化升级规划 · cyp-agent

> 本文是「把数学金融内核从入门级升级到专业级」的总蓝图。回答一个诚实的问题：
> **当前工程强、数学弱**——架构/风控/回测框架到位，但装的量化内核是刻意的占位版本。
> 本文系统编目所有强工具、映射到现有架构插点、分期落地，并给出**量化特有的验证方法论**。
>
> 配套：架构 [ARCHITECTURE.md](ARCHITECTURE.md)、风控 [RISK.md](RISK.md)、多智能体 [AGENTS.md](AGENTS.md)、里程碑 [ROADMAP.md](ROADMAP.md)。

---

## 1. 第一性原理

1. **严谨性优先于花哨 alpha**。加密里头号杀手是**过拟合**，不是"数学不够强"。当前 `sweep` 从 N 组挑最优＝教科书级多重检验陷阱。补的第一件事是「别自欺」的工具（OOS / Deflated Sharpe / PBO），而非更炫的信号。
2. **插件化，不重写**。架构已为此预留插槽：分析师可插拔（`Agent` 协议）、`StrategyConfig` 参数化、风控规则是可组合纯函数、`PortfolioView` 可扩展。每个量化方法都是**加模块**。
3. **降级路径恒存**。重型数学走 `[quant]` extra；缺库时回退到现有轻量实现。"无密钥/无重依赖可跑"这条底线不破。
4. **三档统一**。任何新方法必须能在**回测/模拟/实盘**同一套管线跑（见 [ROADMAP M5](ROADMAP.md)），禁止回测专用的分叉逻辑。
5. **风控永远是确定性护栏**。再强的模型也只在硬护栏内建议；VaR/CVaR 等作为**新增护栏**收紧，绝不放宽既有护栏（见 [RISK.md](RISK.md)）。

## 2. 现状盘点（诚实版）

| 环节 | 现在 | 数学水平 | 目标 |
| --- | --- | --- | --- |
| 信号 | SMA/EMA/RSI/MACD/ATR/BOLL 固定阈值 + 加权投票 | 入门 TA | + 统计验证 / regime / stat-arb |
| 波动率 | ATR（历史均值） | 无预测性 | EWMA / GARCH（波动聚集） |
| 仓位 | 固定分数（风险=账户1%÷止损距离） | 教科书第一章 | 波动率目标 / 分数 Kelly |
| 止损 | ATR×倍数 | 粗糙 | 波动率自适应 / 分位数 |
| 相关性 | **静态**聚类（majors/alt 硬编码） | 非实测 | EWMA / DCC-GARCH 动态相关 |
| 组合 | 簇内同向净敞口上限 | 无协方差 | 均值-方差 / HRP / 风险平价 |
| 风险度量 | 敞口/回撤/连亏上限 | 无尾部度量 | VaR / CVaR / EVT |
| 执行 | 固定滑点 + 市价 | 无冲击模型 | Almgren-Chriss / VWAP |
| 回测 | 单资产单仓 + 网格扫参 | **过拟合风险** | walk-forward / Deflated Sharpe / PBO |

## 3. 能力地图（方法编目）

优先级：**T1**=最高 ROI（补短板），**T2**=强升级，**T3**=专业/可选。依赖列标注需引入的库。

### 3.1 回测严谨性（★ 最关键）

| 方法 | 数学核心 | 插点 | 依赖 | 优先 |
| --- | --- | --- | --- | --- |
| Walk-forward（滚动/锚定） | 时序 OOS，训练→验证滚动前移 | `backtest/validate.py` | numpy | **T1** |
| Purged K-Fold + Embargo | 剔除标签重叠泄漏（López de Prado） | `backtest/validate.py` | numpy | **T1** |
| Combinatorial Purged CV → PBO | 过拟合概率（Prob. of Backtest Overfitting） | `backtest/pbo.py` | numpy/scipy | **T1** |
| Deflated Sharpe Ratio | 校正试验次数 + 非正态的夏普显著性 | `backtest/stats.py` | scipy | **T1** |
| Monte Carlo / Bootstrap | 交易序列重采样 → 收益/回撤置信区间 | `backtest/mc.py` | numpy | **T1** |
| 真实成本模型 | 手续费 + 点差 + 冲击(∝√size) + 资金费 | `backtest/costs.py` | — | **T1** |
| 参数敏感度 / regime 分层绩效 | 稳健性热力图，避免脆弱峰值 | `backtest/robust.py` | numpy | T2 |

### 3.2 波动率与风险度量

| 方法 | 数学核心 | 插点 | 依赖 | 优先 |
| --- | --- | --- | --- | --- |
| EWMA 波动率 | RiskMetrics λ 衰减方差 | `data/volatility.py` | numpy | **T1** |
| GARCH(1,1)/EGARCH | 条件异方差，波动聚集与杠杆效应 | `data/volatility.py` | arch | T2 |
| 历史/参数 VaR | 分位数损失 | `risk/measures.py` | numpy | **T1** |
| CVaR / Expected Shortfall | 尾部条件期望（相干风险测度）→ 护栏 | `risk/rules.py` | numpy | **T1** |
| 极值理论(EVT/POT) | 广义帕累托拟合尾部 | `risk/measures.py` | scipy | T3 |

### 3.3 仓位与资金管理

| 方法 | 数学核心 | 插点 | 依赖 | 优先 |
| --- | --- | --- | --- | --- |
| 波动率目标 | 仓位∝目标波动/预测波动 | `strategist` + `StrategyConfig` | numpy | **T1** |
| 分数 Kelly | f\*=μ/σ²（增长最优），取 ¼–½ Kelly | `strategist` | numpy | T2 |
| 风险预算 / ERC | 各仓风险贡献均衡 | `portfolio/alloc.py` | numpy | T2 |

### 3.4 组合构建

| 方法 | 数学核心 | 插点 | 依赖 | 优先 |
| --- | --- | --- | --- | --- |
| EWMA / Ledoit-Wolf 协方差 | 收缩估计，稳健协方差 | `portfolio/covariance.py` | numpy/sklearn | **T1** |
| 均值-方差(MVO) | 二次规划最优权重 | `portfolio/alloc.py` | cvxpy | T2 |
| HRP 层次风险平价 | 层次聚类 + 递归二分（抗病态协方差） | `portfolio/alloc.py` | scipy | T2 |
| Black-Litterman | 均衡先验 + 观点贝叶斯融合 | `portfolio/alloc.py` | numpy | T3 |

### 3.5 信号 / 统计套利（真 alpha）

| 方法 | 数学核心 | 插点 | 依赖 | 优先 |
| --- | --- | --- | --- | --- |
| Regime 检测（HMM / 马尔可夫切换） | 隐状态：牛/熊/震荡，据此开关策略 | `agents/analysts.py`（新分析师） | hmmlearn/numpy | T2 |
| 协整配对（Engle-Granger/Johansen） | 残差平稳 → 均值回归 stat-arb | `agents/analysts.py` | statsmodels | T2 |
| 卡尔曼滤波 | 动态对冲比/趋势状态估计 | `data/filters.py` | numpy | T2 |
| OU 过程 | 均值回归半衰期 → 入场/持有 | `data/features.py` | numpy | T3 |
| Hurst 指数 | 趋势(>0.5)/回归(<0.5)判别 | `data/features.py` | numpy | T3 |
| 分数阶差分 | 平稳但保记忆的特征 | `data/features.py` | numpy | T3 |

### 3.6 执行 / 微观结构

| 方法 | 数学核心 | 插点 | 依赖 | 优先 |
| --- | --- | --- | --- | --- |
| Almgren-Chriss | 冲击 vs 风险权衡的最优拆单轨迹 | `execution/schedule.py` | numpy | T3 |
| VWAP / TWAP | 成交量/时间加权拆单 | `execution/schedule.py` | — | T2 |
| 订单簿失衡 / microprice | 短周期方向/成交价预测 | `agents/analysts.py` | numpy | T3 |

### 3.7 衍生品（我们做永续）

| 方法 | 数学核心 | 插点 | 依赖 | 优先 |
| --- | --- | --- | --- | --- |
| 资金费/基差期限结构 | carry 与均值回归建模 | `agents/derivatives.py` | numpy | T2 |
| 永续资金费套利 | 现货-永续对冲收 carry | `agents/analysts.py` | — | T3 |
| 期权 Greeks / IV 曲面 | BS/BSM，Δ/Γ/Vega 对冲 | 新模块（仅接期权时） | scipy | T3 |

### 3.8 机器学习（谨慎）

| 方法 | 数学核心 | 插点 | 依赖 | 优先 |
| --- | --- | --- | --- | --- |
| 三重障碍标注 + Meta-labeling | López de Prado，标签质量 > 模型 | `backtest/labeling.py` | numpy | T3 |
| 梯度提升 + 特征重要性 | 非线性、可解释性 | `agents/analysts.py` | lightgbm | T3 |

> ⚠️ ML 在加密极易过拟合：**先把 §3.1 严谨性做扎实再碰 ML**，否则只是更高级的自欺。

## 4. 架构插点（如何接入而不重写）

```
数据层 data/        → volatility.py(EWMA/GARCH) · features.py(OU/Hurst/分数差分) · filters.py(Kalman)
分析师 agents/       → 新分析师(regime/statarb/microstructure) 实现 Agent 协议 → AnalystReport
策略官 strategist    → 仓位法(vol-target/Kelly) 由 StrategyConfig 参数化；数字仍规则确定、LLM 只润色
风控   risk/         → 新纯函数护栏(CVaR/VaR) 加入 ALL_RULES；RiskContext 加字段；只收紧不放宽
组合   portfolio/    → covariance.py(收缩协方差) · alloc.py(MVO/HRP/ERC)；PortfolioView 扩展
执行   execution/    → schedule.py(Almgren-Chriss/VWAP) 供交易员拆单
回测   backtest/     → validate.py/pbo.py/stats.py/mc.py/costs.py —— 严谨性套件
```

**插件契约**（沿用现有）：
- 新分析师 = 一个实现 `async run(snapshot, ctx) -> AnalystReport` 的类 + 注册进 `ANALYSTS` + 降级路径 + 单测。
- 新护栏 = `risk/rules.py` 一个纯函数 `(proposal, ctx, cfg) -> RuleResult` + 加入 `ALL_RULES` + 边界单测。
- 新仓位法 = `StrategyConfig` 加字段 + 策略官内分支；回测扫参自动覆盖。
- 新组合优化 = `portfolio/alloc.py` 一个函数，输入协方差/预期收益 → 目标权重；风控引擎仍是最终裁决。

## 5. 分期路线（Q 线，与里程碑并行）

### Q1 · 反自欺 + 波动率内核（T1）
**目标**：让回测可信、波动率有预测性、尾部风险入护栏。
- [ ] `backtest/validate.py`：walk-forward（滚动/锚定）+ purged K-fold + embargo
- [ ] `backtest/stats.py`：Deflated Sharpe Ratio + 最小回测长度
- [ ] `backtest/pbo.py`：CPCV → PBO 过拟合概率
- [ ] `backtest/mc.py` + `backtest/costs.py`：bootstrap 置信区间 + 真实成本(手续费+点差+冲击√size+资金费)
- [ ] `data/volatility.py`：EWMA 波动率 → 波动率目标仓位 + 波动自适应止损
- [ ] `risk/measures.py` + 护栏：历史 VaR / **CVaR 上限**（新增确定性护栏）
- [ ] `portfolio/covariance.py`：EWMA/Ledoit-Wolf 协方差 → **实测相关性**替换静态聚类

**验收**：同一策略在 walk-forward OOS 上给出 Deflated Sharpe 与 PBO；扫参择优改为「OOS + PBO 门槛」而非样本内最优；CVaR 超限能否决；相关性护栏用实测协方差。

### Q2 · 更优仓位 + 状态识别 + 组合优化（T2）
- [ ] 分数 Kelly 仓位（¼–½，与波动率目标二选一/融合）
- [ ] GARCH(1,1) 波动率（`arch`）
- [ ] Regime 分析师（HMM）：震荡市降杠杆/观望
- [ ] `portfolio/alloc.py`：HRP + 风险平价（ERC）多资产权重
- [ ] 衍生品分析师升级：资金费/基差期限结构 carry 模型
- [ ] VWAP/TWAP 拆单

**验收**：多资产组合按 HRP 分配并过组合护栏；regime 分层绩效显示震荡市回撤下降；GARCH 波动率回测优于 ATR。

### Q3 · 统计套利 + 微观结构（T2/T3）
- [ ] 协整配对分析师（Engle-Granger/Johansen）+ 卡尔曼动态对冲比
- [ ] OU 半衰期 / Hurst / 分数阶差分特征
- [ ] 订单簿失衡 / microprice 短周期信号
- [ ] Almgren-Chriss 最优执行

### Q4 · 进阶（T3，视需要）
- [ ] EVT 尾部风险、Copula 相依
- [ ] 三重障碍标注 + meta-labeling + GBDT（严格 OOS/PBO 把关下）
- [ ] Black-Litterman、期权 Greeks（若接期权）

## 6. 验证方法论（量化的命门 · 强制标准）

任何策略/参数上线前必须过下列关卡，**这是本升级最有价值的部分**：

1. **无前视不变量**：回测只用「截至当前 bar」信息（现 `HistoricalData` 已窗口化，形式化为断言）；特征计算不得引用未来。
2. **训练/验证/测试三分 + Walk-forward**：滚动或锚定前移，报告**样本外(OOS)**绩效；样本内结果仅供参考。
3. **Purged K-Fold + Embargo**：标签有重叠/自相关时，剔除训练-测试边界样本，防泄漏（López de Prado《Advances in Financial ML》）。
4. **过拟合量化**：CPCV → **PBO**（过拟合概率）；扫参择优的入选门槛是「PBO < 阈值 且 OOS 显著」，不是样本内最高分。
5. **Deflated Sharpe Ratio**：按试验次数 N、偏度、峰度校正夏普显著性；N 越大，达标门槛越高（直接治我们 `sweep` 的多重检验病）。
6. **Monte Carlo/置换检验**：bootstrap 交易序列 → 收益/最大回撤的置信区间与破产概率；置换检验区分 alpha 与运气。
7. **真实成本**：手续费 + 点差 + 市场冲击(∝√下单量) + 永续资金费；成本敏感度必测（很多"策略"扣成本即失效）。
8. **稳健性**：参数敏感度热力图（选平台不选尖峰）+ regime 分层绩效（牛/熊/震荡分别看）。
9. **实盘-回测对账**：`paper` 影子并行，跟踪实盘滑点/成交与回测假设的偏差（对接 [RUNTIME.md](RUNTIME.md) 监控）。

## 7. 依赖策略

保持核心轻量，重型数学入 extras（`pyproject.toml`）：

```toml
[project.optional-dependencies]
quant = ["numpy>=1.26", "scipy>=1.11", "pandas>=2.2"]      # 基础数值/统计
quant-full = ["statsmodels>=0.14", "arch>=6.3",            # 协整/GARCH
              "scikit-learn>=1.4", "cvxpy>=1.4"]           # 收缩协方差/凸优化
```

- 缺 `[quant]` 时：波动率回退 ATR、相关性回退静态聚类、回测回退当前简单版——**功能降级不崩**。
- 每个方法在导入处惰性检测依赖，给出清晰安装提示（对齐 `ccxt`/`anthropic` 的做法）。

## 8. 反模式（明确不做）

- ❌ **样本内择优即上线**（当前 `sweep` 的隐患）——必须 OOS + PBO 把关。
- ❌ **数据窥探**：反复在同一测试集调参直到好看。
- ❌ **忽略非平稳性**：加密结构漂移快，静态模型需滚动重估。
- ❌ **成本乐观**：不建模冲击/资金费的回测普遍虚高。
- ❌ **相关性=1 的伪分散**：多个高相关多头当分散（现静态聚类是权宜，Q1 用实测协方差替换）。
- ❌ **ML 先行**：严谨性未立就上 ML = 更高级的过拟合。
- ❌ **让模型越过硬护栏**：再强的 alpha 也只能在确定性风控内建议。

## 9. 与现有文档的关系

- 本文是 [ROADMAP.md](ROADMAP.md) 的**量化深化线**（Q1–Q4），与 M 里程碑（工程/市场覆盖）并行推进。
- 新增护栏（VaR/CVaR）并入 [RISK.md](RISK.md) 硬护栏清单。
- 新分析师并入 [AGENTS.md](AGENTS.md) 分析师团。
- 验证方法论（§6）是所有策略进入 `live` 前的强制关卡，纳入 [KICKOFF.md](KICKOFF.md) 的 Definition of Done。
