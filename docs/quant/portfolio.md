# 组合与协方差模型

目标：从“逐笔交易风控”升级到“账户组合风控”。当前已有 `PortfolioView`、总敞口、单标的集中度、相关性簇同向敞口护栏；本分册描述下一步数学化方案。

## 收益矩阵

多资产收益矩阵：

```text
R[t, i] = price[t, i] / price[t-1, i] - 1
```

要求：

- 时间索引对齐，缺失值不能用未来填充。
- 不同交易所同一资产先合并为统一标的风险因子。
- 稳定币、抵押品、合约保证金单独建风险因子。

## EWMA 协方差

递推：

```text
Sigma_t = lambda * Sigma_{t-1} + (1 - lambda) * r_t r_t'
```

默认：

```text
lambda = 0.94
min_samples = 120
```

用途：

- 替换静态 `majors/alt` 相关性聚类。
- 计算组合波动、边际风险贡献、相关性热力图。

## 收缩协方差

样本协方差在资产多、样本少时病态。Ledoit-Wolf 思路：

```text
Sigma_shrunk = delta * F + (1 - delta) * S
```

其中 `S` 是样本协方差，`F` 是结构化目标矩阵。没有 sklearn 时，先用固定 `delta` 的对角收缩降级：

```text
F = diag(S)
delta = 0.10..0.50
```

## 风险贡献

组合权重 `w`，协方差 `Sigma`：

```text
portfolio_vol = sqrt(w' Sigma w)
MRC_i = (Sigma w)_i / portfolio_vol
RC_i = w_i * MRC_i
```

风险平价目标：

```text
RC_i ~= total_risk / n
```

## HRP

HRP（Hierarchical Risk Parity）步骤：

1. 用相关矩阵得到距离：

```text
d_ij = sqrt(0.5 * (1 - corr_ij))
```

2. 层次聚类，得到资产排序。
3. 递归二分，每次按簇方差分配权重。

优点：不需要求逆协方差，适合样本短、相关性不稳定的加密组合。

## MVO

均值-方差优化：

```text
maximize  mu' w - lambda * w' Sigma w
subject to sum(w) = 1
           0 <= w_i <= max_weight_i
```

实盘限制：

- `mu` 必须来自 OOS，不能用全样本均值。
- 权重结果只是目标组合，执行前仍需风控和审批。
- 若协方差条件数过大，降级为 HRP 或等风险预算。

## Black-Litterman

用途：把市场均衡先验和策略观点结合，降低主观 alpha 噪声。

```text
posterior_return = BayesianUpdate(prior_return, views, confidence)
```

适合 M6 之后多策略组合，不作为近期核心。

## 测试清单

- 完全相关资产不能被当作分散。
- 两个高相关多头应触发相关簇同向敞口上限。
- 协方差样本不足时降级静态聚类，并标记 degraded。
- HRP 权重非负、和为 1、单资产不超过上限。
- MVO 在病态协方差下必须失败隔离或降级。
