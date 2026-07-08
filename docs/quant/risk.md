# 尾部风险模型

目标：把“最坏时候会亏多少”变成确定性护栏，而不是只看平均收益和普通回撤。本分册对应 `risk/measures.py` 与 `risk/rules.py`。

当前实现状态：

| 能力 | 状态 | 代码 |
| --- | --- | --- |
| 非负损失序列 | 已实现 | `losses_from_returns` |
| Historical VaR | 已实现 | `historical_var` |
| CVaR / Expected Shortfall | 已实现 | `conditional_value_at_risk` |
| 计价币尾部风险 | 已实现 | `tail_risk_quote` |
| CVaR 硬护栏 | 已实现 | `rule_cvar_limit` |
| 参数 VaR / EVT | 待实现 | 后续扩展 |

## 损失定义

统一用正数表示损失：

```text
loss_t = - portfolio_return_t
```

若使用金额口径：

```text
loss_quote_t = - pnl_quote_t
```

所有 VaR/CVaR 必须标明：

| 字段 | 说明 |
| --- | --- |
| `horizon` | 风险窗口，如 1 bar、1 day |
| `confidence` | 置信度，如 95%、99% |
| `method` | historical / parametric / EVT |
| `sample_size` | 估计样本数 |

## Historical VaR

历史 VaR 是损失分布的分位数：

```text
VaR_alpha = quantile(losses, alpha)
```

默认：

```text
alpha = 0.95 for paper
alpha = 0.99 for live risk dashboard
min_samples = 250
```

缺点：对未发生过的尾部不敏感；样本短时容易低估。

## Parametric VaR

正态近似：

```text
VaR_alpha = mu_loss + z_alpha * sigma_loss
```

仅可作为对照，不作为加密实盘唯一护栏。加密收益厚尾、跳跃、非平稳，正态 VaR 容易过度乐观。当前尚未实现。

## CVaR / Expected Shortfall

CVaR 是超过 VaR 后的平均损失：

```text
CVaR_alpha = mean(loss | loss >= VaR_alpha)
```

它比 VaR 更适合作为护栏，因为它关心尾部严重程度。默认护栏：

```text
CVaR_95 <= equity * daily_drawdown_limit
CVaR_99 <= equity * max_drawdown_limit / 3
```

当前硬护栏：

```text
portfolio_cvar_quote <= equity_quote * max_cvar_pct
default max_cvar_pct = 0.03
```

规则语义：

- 超过 CVaR 上限：拒绝新开仓。
- 接近上限 80%：允许降仓，不允许加杠杆。
- 样本不足：降级为现有敞口/回撤硬护栏，不放宽。

## EVT / POT

POT（Peaks over Threshold）只建模超过阈值的尾部：

```text
excess = loss - threshold, loss > threshold
excess ~ Generalized Pareto Distribution
```

用途：

- 对极端行情做压力测试。
- 给合约杠杆和链上仓位设置额外折扣。

默认状态：Q4 可选。未实现前不能作为 live 通过依据。

## 压力测试

每次策略候选至少跑：

| 场景 | 价格冲击 | 资金费/成本 | 目的 |
| --- | --- | --- | --- |
| 普通压力 | `2 * realized_vol` | `2x` | 检查成本敏感 |
| 快速崩盘 | `-8%..-15%` | `3x` | 检查止损和熔断 |
| 流动性塌陷 | 价差 `5x` | 冲击 `sqrt(size)` | 检查大单不可成交 |
| 相关性趋一 | 相关簇同向同时亏损 | 正常 | 检查伪分散 |

## 测试清单

- 历史 VaR 对已知损失序列返回正确分位数。
- CVaR 必须大于或等于同置信度 VaR。
- 样本不足时返回 `degraded=true`，风控不得因此放宽。
- CVaR 超限应触发 `RiskAssessment.verdict = rejected`。
- 压力测试应能复现“高相关多头同时下跌”的拒绝路径。
