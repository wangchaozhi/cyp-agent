# 绩效统计：Sharpe、PSR、DSR 与 MinTRL

本分册对应 `internal/backtest/stats.go`。实现只依赖 Go 标准库，所有函数使用单周期简单收益，当前不会自动年化。

## 1. 输入

设收益序列为 \(r_1,\ldots,r_n\)。调用者必须保证：

- 至少两个观测；不足时统计函数返回安全值或无穷长度。
- 输入全部有限，且使用同一频率。
- 回测收益已经扣除成本后再用于上线研究；当前基础回测尚未计成本，必须显式注明。
- 自相关显著时，不能把朴素 Sharpe 当成独立同分布证据。

## 2. Sharpe

实现使用总体标准差：

\[
\hat S=\frac{\bar r}{\sqrt{\frac{1}{n}\sum_{t=1}^{n}(r_t-\bar r)^2}}
\]

标准差为零或观测不足时返回 0。若需要年化，由报告层依据真实周期乘以 \(\sqrt{N}\)，不能在不知道频率时硬编码。

Go API：

```go
value := backtest.Sharpe(returns)
```

## 3. Probabilistic Sharpe Ratio

PSR 衡量真实 Sharpe 超过基准 \(S^*\) 的概率。实现先计算总体偏度 \(\gamma_3\) 和总体峰度 \(\gamma_4\)，再使用：

\[
z=\frac{(\hat S-S^*)\sqrt{n-1}}
{\sqrt{1-\gamma_3\hat S+\frac{\gamma_4-1}{4}\hat S^2}}
\]

\[
PSR=\Phi(z)
\]

分母使用 `max(1e-12, value)` 防止数值崩溃。`NormCDF` 使用 `math.Erf`；`NormPPF` 使用 Acklam 有理近似并明确处理 0/1 边界。

```go
probability := backtest.ProbabilisticSharpe(returns, benchmark)
```

推荐研究门：`PSR >= 0.95`，但阈值必须和样本频率、成本假设及策略用途一起解释。

## 4. Expected Max Sharpe 与 Deflated Sharpe

多次试验会把“最佳 Sharpe”向上推。`ExpectedMaxSharpe` 根据所有候选试验 Sharpe 的总体标准差 \(\sigma_S\) 和试验数 \(N\) 估计随机搜索下的最佳值：

\[
E[S_{max}] \approx \sigma_S\left[(1-\gamma)\Phi^{-1}(1-1/N)
+\gamma\Phi^{-1}(1-1/(Ne))\right]
\]

其中 \(\gamma\) 是 Euler–Mascheroni 常数。DSR 再计算最佳策略超过这个基准的 PSR：

\[
DSR=PSR(\hat S, E[S_{max}])
\]

```go
benchmark := backtest.ExpectedMaxSharpe(allTrialSharpes)
dsr := backtest.DeflatedSharpe(selectedReturns, allTrialSharpes)
```

试验集合必须包含真实尝试过的全部候选，不能只传入最终展示的少数结果。当前 `RobustSweep` 默认以 `DSR >= 0.5` 作为联合通过条件之一；研究上线通常应采用更严格阈值。

## 5. Minimum Track Record Length

MinTRL 反推让 Sharpe 超过基准并达到目标置信度所需的最少观测数：

\[
MinTRL=1+\left[1-\gamma_3\hat S+
\frac{\gamma_4-1}{4}\hat S^2\right]
\left(\frac{\Phi^{-1}(p)}{\hat S-S^*}\right)^2
\]

当观测不足或 \(\hat S\le S^*\) 时返回正无穷。

```go
needed := backtest.MinTrackRecordLength(returns, benchmark, 0.95)
```

## 6. 报告要求

每份策略报告至少包含：

- 收益频率、样本数、起止时间和缺口处理；
- 总收益、最大回撤、未年化/年化 Sharpe 的明确标签；
- PSR 的基准值、DSR 的总试验数、MinTRL 的目标概率；
- 样本外收益、PBO、交易数和真实成本假设；
- 代码版本、参数、seed 和数据版本。

禁止只报告最佳 Sharpe 或把 `DSR > 0` 解释为“策略有效”。

## 7. 测试清单

对应测试位于 `internal/backtest/stats_test.go`：

- `NormCDF(NormPPF(p))` 在代表性概率上近似还原 `p`。
- 常数收益、单点和短序列不产生 NaN。
- 收益整体改善时 Sharpe/PSR 单调上升。
- 试验数量或候选离散度增加时 Expected Max Sharpe 不应下降。
- `Sharpe <= benchmark` 时 MinTRL 为正无穷。
- 所有 JSON 报告只包含有限数值。
