# 统计显著性与反过拟合指标

本分册对应 `core/cyp/backtest/stats.py`。当前实现为纯 Python，无 scipy 依赖，包含 `Sharpe`、`PSR`、`Deflated Sharpe`、`MinTRL` 和正态 CDF/PPF 近似。

## 收益与 Sharpe

单周期收益：

```text
r_t = equity_t / equity_{t-1} - 1
```

非年化 Sharpe：

```text
SR = mean(r) / std(r)
```

如果 `std(r) = 0`，当前实现返回 `0`，避免把恒定序列误判为无限好。年化 Sharpe 只在周期稳定时计算：

```text
SR_annual = SR_period * sqrt(periods_per_year)
```

加密市场 7x24，周期默认：

| bar | `periods_per_year` |
| --- | --- |
| 1m | `365 * 24 * 60` |
| 5m | `365 * 24 * 12` |
| 1h | `365 * 24` |
| 1d | `365` |

## PSR

PSR（Probabilistic Sharpe Ratio）回答：观测 Sharpe 大于某个基准 Sharpe 的概率。

令：

```text
SR      = 观测 Sharpe
SR*     = 基准 Sharpe
gamma3  = 偏度
gamma4  = 峰度，正态约为 3
n       = 样本数
```

统计量：

```text
z = (SR - SR*) * sqrt(n - 1)
    / sqrt(1 - gamma3 * SR + (gamma4 - 1) / 4 * SR^2)

PSR = Phi(z)
```

默认门槛：

| 场景 | 门槛 |
| --- | --- |
| 研究候选 | `PSR >= 0.80` |
| paper 候选 | `PSR >= 0.90` |
| live 候选 | `PSR >= 0.95` 且必须 OOS |

## Deflated Sharpe

普通 Sharpe 会被多重试验抬高。若从 `N` 组参数里挑最优，基准不应是 `0`，而应是“随机试验下期望最大 Sharpe”。

当前实现：

```text
E[max SR] = std(SR_trials) * ((1 - gamma) * z_1 + gamma * z_2)
z_1 = Phi^-1(1 - 1/N)
z_2 = Phi^-1(1 - 1/(N * e))
gamma = Euler constant

DSR = PSR(returns, E[max SR])
```

默认门槛：

| DSR | 结论 |
| --- | --- |
| `< 0.50` | 没有显著边际 |
| `0.50..0.80` | 只可研究观察 |
| `0.80..0.95` | 可进入 paper |
| `>= 0.95` | 统计上较强，但仍需 PBO/成本/OOS |

## MinTRL

MinTRL（Minimum Track Record Length）估算达到目标置信度所需样本数：

```text
MinTRL = 1 + A * (Phi^-1(p) / (SR - SR*))^2
A = 1 - gamma3 * SR + (gamma4 - 1) / 4 * SR^2
```

如果 `SR <= SR*`，返回无穷大，表示再短的样本都不能证明有边际。

使用规则：

- 若 `actual_n < MinTRL`，策略只能进入观察池。
- 若 `MinTRL` 远大于可获得历史长度，降低模型复杂度。
- MinTRL 不能替代 OOS；它只回答样本长度是否够。

## 输出与仪表盘

建议所有回测报告输出：

```json
{
  "sharpe": 0.7,
  "psr": 0.92,
  "dsr": 0.81,
  "min_track_record_length": 240,
  "n_returns": 360,
  "n_trials": 24
}
```

## 测试清单

- `norm_cdf(0) == 0.5`，`norm_ppf(norm_cdf(x)) ~= x`。
- 正收益序列 Sharpe 为正，取反后为负。
- 同分布样本变长时 PSR 应上升。
- 增加试验次数后 DSR 应低于原始 PSR。
- 无边际策略的 MinTRL 返回无穷大。
