# 量化模型规格索引

本目录把 [../QUANT.md](../QUANT.md) 的蓝图拆成可实现、可验收的数学规格。原则是：每个模型必须说明输入、公式、默认参数、验收阈值、降级路径和测试要求。

## 阅读顺序

| 分册 | 内容 | 当前状态 |
| --- | --- | --- |
| [validation.md](validation.md) | 无前视、walk-forward、purged K-fold、PBO | `validate.py` / `pbo.py` 已有纯 Python 实现 |
| [stats.md](stats.md) | Sharpe、PSR、Deflated Sharpe、MinTRL | `stats.py` 已有纯 Python 实现 |
| [risk.md](risk.md) | VaR、CVaR、EVT、尾部护栏 | Historical VaR / CVaR + CVaR 护栏已实现；EVT 待实现 |
| [sizing.md](sizing.md) | 波动率目标、分数 Kelly、止损自适应 | EWMA + vol-target 已有实现；Kelly 待实现 |
| [portfolio.md](portfolio.md) | 协方差、HRP、ERC、MVO、Black-Litterman | 组合硬护栏已有；优化器待实现 |
| [signals_execution.md](signals_execution.md) | regime、协整、Kalman、微观结构、执行模型 | 待实现 |

## 强制验收口径

任何量化模型进入 `paper` 或 `live` 前，至少满足：

1. **无前视**：特征、标签、参数估计只使用当前 bar 及以前数据。
2. **样本外报告**：必须报告 walk-forward OOS 结果；样本内只作诊断。
3. **反过拟合**：扫参择优必须报告 PSR/DSR/PBO，不能只按样本内收益排序。
4. **成本敏感**：策略结果必须能在手续费、点差、滑点、资金费压力下复算。
5. **护栏优先**：数学模型只产生建议或更严格阈值，不能放宽 `risk/` 的确定性硬护栏。
6. **降级可跑**：缺少重型依赖时要回退到当前轻量实现，不能破坏零密钥离线闭环。

## 默认通过阈值

| 指标 | 默认门槛 | 说明 |
| --- | --- | --- |
| PSR | `>= 0.95` | 观测 Sharpe 显著大于基准的概率 |
| DSR | `>= 0.80` | 多重试验校正后仍有统计可信度 |
| PBO | `<= 0.20` | 超过 20% 说明参数选择过拟合风险偏高 |
| OOS 最大回撤 | `<= 风控上限` | 至少不能超过 `RiskConfig` 同级熔断阈值 |
| 成本压力后收益 | `> 0` | 默认成本和 2x 成本下都应检查 |

这些阈值是保守默认值，不是盈利承诺。实盘前可以更严格，不能更宽松。
