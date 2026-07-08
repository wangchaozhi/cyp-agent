# 仓位与资金管理

目标：让仓位大小由风险预算和波动状态决定，而不是固定金额或固定杠杆。本分册对应 `StrategyConfig`、`Strategist` 和未来的 sizing 模块。

## 固定风险预算

当前默认仓位：

```text
risk_budget = equity * max_risk_per_trade
stop_frac   = abs(entry - stop) / entry
size_quote  = risk_budget / stop_frac
```

优点：简单、可解释、由止损距离约束。缺点：波动突增时 ATR 反应较慢，容易在 regime 切换时仓位过大。

## EWMA 波动率

当前已实现 EWMA 每周期波动率：

```text
sigma_t^2 = lambda * sigma_{t-1}^2 + (1 - lambda) * r_t^2
sigma_t   = sqrt(sigma_t^2)
```

默认：

```text
lambda = 0.94
```

使用：

- `stop_mode="vol"`：止损距离使用 `entry * sigma_t * k_stop`。
- `vol_target`：仓位按目标波动缩放。

## 波动率目标仓位

核心公式：

```text
size_quote = equity * target_vol / forecast_vol
```

防护：

```text
size_quote <= equity * max_position_pct
gross_exposure <= equity * max_gross_exposure
leverage <= max_leverage
```

默认门槛：

| 参数 | 默认建议 |
| --- | --- |
| `target_vol` | `0.01..0.02` 每 bar，按数据周期校准 |
| `forecast_vol floor` | `0.002`，避免低波动放大仓位 |
| `max_size_multiplier` | `2x` 默认固定风险仓位 |

当前代码已有 `StrategyConfig.vol_target`，但仍应由风控硬护栏最终裁决。

## 分数 Kelly

Kelly 估计增长最优仓位：

```text
f* = mu / sigma^2
```

其中 `mu` 是期望超额收益，`sigma^2` 是收益方差。实盘只允许分数 Kelly：

```text
f = kelly_fraction * f*, kelly_fraction in [0.25, 0.50]
```

默认规则：

- `mu` 必须来自 OOS 或 walk-forward，不能来自全样本回测。
- 若 `PSR < 0.95` 或 `PBO > 0.20`，Kelly 仓位禁用。
- Kelly 输出只能收紧或替代仓位建议，不能越过 `RiskConfig` 上限。

## 止损自适应

ATR 止损：

```text
stop_distance = ATR * k_stop
```

EWMA 止损：

```text
stop_distance = entry_price * sigma_ewma * k_stop
```

建议：

| 市场状态 | 止损模型 |
| --- | --- |
| 平稳趋势 | ATR 或 EWMA 均可 |
| 波动突增 | EWMA 优先 |
| 流动性差 | 加大 `k_stop`，同时降低仓位 |
| 合约高杠杆 | 止损距离必须远离爆仓缓冲 |

## 测试清单

- 波动率上升时，同一 `target_vol` 下仓位应下降。
- `forecast_vol = 0` 或样本不足时回退固定风险预算。
- Kelly 在负期望或低置信下返回 `0` 或禁用。
- 止损距离变化必须反映在 `size_quote` 上。
- 所有 sizing 结果必须继续经过 `risk.engine`。
