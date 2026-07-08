# 信号、统计套利与执行模型

目标：把 alpha 研究和执行建模纳入同一套风控边界。本文列出的模型都不能绕过审批门和硬护栏。

## Regime 检测

候选模型：

| 模型 | 状态 |
| --- | --- |
| HMM | 隐状态：趋势、震荡、高波动 |
| Markov Switching | 均值/方差随状态变化 |
| 规则降级 | 波动率分位数 + 均线斜率 |

输出契约：

```json
{
  "agent": "regime",
  "stance": "neutral",
  "confidence": 0.7,
  "signals": [
    {"name": "regime", "value": "high_vol"}
  ],
  "degraded": false
}
```

风控用法：

- 高波动：降低 `max_leverage`、提高止损缓冲、降低 `vol_target`。
- 震荡：趋势策略降权，均值回归策略可进入候选。
- 状态不稳定：不开新仓或仅 paper 观察。

## 协整配对

Engle-Granger 流程：

1. 对 `y` 和 `x` 回归：`y_t = alpha + beta x_t + e_t`。
2. 检查残差 `e_t` 平稳。
3. 用 z-score 生成均值回归信号：

```text
z_t = (e_t - mean(e)) / std(e)
```

默认：

| 参数 | 默认 |
| --- | --- |
| 入场 | `abs(z) >= 2.0` |
| 出场 | `abs(z) <= 0.5` |
| 失效 | `abs(z) >= 4.0` 或平稳性失效 |

验收：

- 必须 OOS 验证半衰期和回归速度。
- 两腿都必须有可成交流动性。
- 组合风控按净敞口和毛敞口同时校验。

## Kalman 动态对冲比

状态：

```text
beta_t = beta_{t-1} + noise
y_t = alpha_t + beta_t x_t + observation_noise
```

用途：

- 配对交易动态 hedge ratio。
- 趋势状态平滑。

降级：缺 numpy/statsmodels 时使用滚动 OLS 或固定 beta。

## 微观结构信号

订单簿失衡：

```text
imbalance = (bid_size_1 - ask_size_1) / (bid_size_1 + ask_size_1)
```

Microprice：

```text
microprice = (ask * bid_size + bid * ask_size) / (bid_size + ask_size)
```

限制：

- 只用于短周期入场质量或拆单，不独立触发大额方向仓。
- 必须检查盘口深度、延迟和撤单风险。

## 执行模型

TWAP：

```text
slice_size = total_size / n_slices
```

VWAP：

```text
slice_size_t = total_size * expected_volume_share_t
```

Almgren-Chriss：

```text
minimize expected_impact + risk_aversion * variance
```

默认接入顺序：

1. TWAP：低依赖、先实现。
2. VWAP：需要成交量曲线。
3. Almgren-Chriss：需要冲击参数标定。

## 测试清单

- Regime 分析师缺库时降级为规则状态，不阻断主流程。
- 协整策略必须在合成协整序列上触发，在独立随机游走上拒绝。
- Kalman/OLS hedge ratio 不得使用未来价格。
- TWAP/VWAP 总下单量必须等于目标量，单片不超过最小/最大下单限制。
- 执行模型只能降低冲击，不能扩大风控批准的总仓位。
