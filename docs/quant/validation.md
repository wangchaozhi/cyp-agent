# 时间序列验证与过拟合概率

本分册对应 `internal/backtest/validation.go` 和 `internal/backtest/optimize.go`。目标是阻止前视、标签重叠和重复调参把噪声包装成策略。

## 1. Walk-forward

`WalkForwardSplits(n, splitCount, minimumTrain, anchored)` 返回半开区间：

```text
[TrainStart, TrainEnd) → [TestStart, TestEnd)
```

- `anchored=true`：训练集固定从 0 开始并逐步扩张。
- `anchored=false`：训练窗口长度固定为 `minimumTrain` 并向前滚动。
- 测试集始终位于训练集之后，不随机打乱。
- 非法或过小参数会安全归一化；无数据时返回空切分。

```go
splits := backtest.WalkForwardSplits(len(returns), 4, 240, true)
```

训练阶段只允许选参数；阈值、归一化、特征和模型都必须冻结后再看对应测试窗。

## 2. Purged K-fold 与 embargo

金融标签常跨越多个 bar。普通 K-fold 即使按时间分组，训练样本仍可能和测试标签窗口重叠。`PurgedKFoldSplits` 对每个测试区间两侧删除 embargo 范围：

```go
splits := backtest.PurgedKFoldSplits(len(samples), 5, 0.01)
```

当前 `embargo` 是总样本数的比例，内部转换为 `int(float64(n) * embargo)`。返回值包含显式的训练和测试索引；调用者仍需根据标签跨度选择足够的 embargo，函数不会推断 holding period。

不变量：

- train 与 test 不相交；
- embargo 区域不出现在 train；
- 每个样本最多进入一个测试 fold；
- 输出顺序保持时间顺序。

## 3. PBO

`ProbabilityBacktestOverfit` 接受矩阵 `strategyReturns[strategy][time]`：

1. 截断到所有策略共有的最短长度。
2. 把时间轴切为偶数个连续 segments。
3. 枚举一半 segments 作为样本内，其余作为样本外。
4. 在样本内选择指标最优策略。
5. 计算该策略在样本外的相对排名。
6. 样本外排名落在中位数以下时计为一次过拟合。

最终：

\[
PBO=\frac{\text{样本外排名落入下半区的组合数}}
{\text{有效组合总数}}
\]

默认指标是 `Sharpe`。策略不足两个、数据为空或无法形成有效分段时安全返回 0；这代表“无法估计”，不应被展示为过拟合风险为零。

```go
pbo := backtest.ProbabilityBacktestOverfit(strategyReturnMatrix, 6, nil)
```

经验阈值：`PBO <= 0.5` 才有资格进入下一道研究门。样本太短时必须同时标记 `insufficient_data`，不能只看数值。

## 4. 当前 RobustSweep

`RobustSweep` 用合成数据执行一个轻量稳健性门：

- 默认前 70% bars 做样本内选择；
- 所有配置的样本内收益用于 PBO 和 trial Sharpe；
- 最佳配置用不同时间偏移的 seed 在后 30% 长度上做 OOS；
- 通过条件：`PBO <= pboMaximum`、OOS 收益为正、`DSR >= dsrMinimum`。

这个实现适合迁移验收，但并不是严格的同一条真实时间序列切片：OOS 合成段通过偏移 seed 重建。真实策略研究应把原始蜡烛切成连续 IS/OOS，并在 `RunCandles` 上运行相同流程。

## 5. 无前视要求

- bar `t` 的决策只能读取 `<= t` 的数据；成交假设必须发生在 `t+1` 或显式使用当期可成交价格。
- 指标 warm-up 后才能产生信号，不能以零值填补并参与训练。
- 标准化、缺失填补和特征选择只在训练窗拟合。
- OHLCV 抓取、清洗和复权规则必须版本化。
- 策略候选数包含人工尝试，不得在最终报告中删掉失败试验。
- 同一 OOS 区间一旦反复用于调参，就不再是 OOS，需要新的 holdout。

## 6. 推荐研究门

| 指标 | 最低要求 | 说明 |
| --- | --- | --- |
| OOS 总收益 | `> 0` | 必须扣除成本后仍为正 |
| PBO | `<= 0.5` | 越低越好；样本不足单独拒绝 |
| DSR | `>= 0.5`（迁移门） | 正式研究建议更严格 |
| 交易数 | 业务阈值 | 少量交易不能依赖渐近统计 |
| 最大回撤 | 风险预算内 | 需同时给置信区间和压力结果 |

任一门失败都返回 `REJECT`，但所有门通过也不等于允许真实执行。

## 7. 测试清单

对应测试位于 `internal/backtest/stats_test.go` 和 `optimize_test.go`：

- Walk-forward 的 train 永远早于 test，anchored/rolling 边界正确。
- Purged split 的 train/test/embargo 无交集。
- PBO 输入长度不一致时按共有区间处理且不 panic。
- 对构造的“样本内冠军、样本外失败”矩阵，PBO 明显升高。
- `RobustSweep` 固定 seed 可复现，输出只含有限值。
- OOS 亏损、PBO 超限或 DSR 不足时结论必须拒绝。
