# 回测验证与防泄漏

目标：证明一个策略不是靠未来信息、样本内调参或偶然噪声“看起来赚钱”。本分册对应 `core/cyp/backtest/validate.py` 与 `core/cyp/backtest/pbo.py`。

## 数据契约

输入是一条按时间升序排列的收益或 bar 序列：

```text
x[0], x[1], ..., x[t]
```

任何在 `t` 时刻产生的特征、信号、仓位和风控判断，只能依赖 `0..t`。如果标签覆盖未来区间 `[t, t+h]`，交叉验证时必须 purge/embargo，避免训练集和测试集标签重叠。

## Walk-Forward

锚定式：

```text
train = [0, train_end)
test  = [train_end, test_end)
```

滚动式：

```text
train = [train_end - window, train_end)
test  = [train_end, test_end)
```

默认：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `n_splits` | `4` | 至少 4 个 OOS 折 |
| `anchored` | `true` | 初期用扩张窗口更稳定 |
| `min_train` | `n // (n_splits + 1)` | 训练样本不足则不评估 |

验收：

- 每个折必须满足 `train_end <= test_start`。
- OOS 指标单独汇总，不与 IS 混算。
- 若 OOS 折少于 3 个，不允许进入 live 候选。

## Purged K-Fold + Embargo

普通 K-Fold 会随机打乱时序，这是交易回测的地雷。Purged K-Fold 要求测试折连续，训练集剔除测试折两侧邻近样本：

```text
purge_lo = test_start - embargo_n
purge_hi = test_end + embargo_n
train = all_indices - [purge_lo, purge_hi)
```

默认：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `k` | `5` | 5 折连续切分 |
| `embargo` | `0.01` | 样本数的 1%，可按标签持有期调大 |

验收：

- 测试折覆盖全集且不重叠。
- 训练集不得包含测试折和 embargo 区间。
- 有持仓期标签时，`embargo_n >= max_holding_bars` 更保守。

## PBO

PBO（Probability of Backtest Overfitting）衡量“样本内最优”在样本外掉到中下游的概率。

流程：

1. 将时间序列切成偶数个 `S` 段。
2. 枚举一半段作为 IS，其余作为 OOS。
3. 每次在 IS 上挑表现最好的策略。
4. 计算该策略在 OOS 的相对排名 `w`。
5. 令 `lambda = log(w / (1 - w))`。
6. `PBO = P(lambda <= 0)`。

解释：

| PBO | 结论 |
| --- | --- |
| `<= 0.10` | 参数选择较稳健 |
| `0.10..0.20` | 可进入 paper 观察 |
| `0.20..0.50` | 高过拟合风险，需要降复杂度或扩样本 |
| `> 0.50` | 基本是在挑噪声 |

默认：

```text
S = 6
metric = sharpe
gate = PBO <= 0.20
```

## 测试清单

- 合成一个恒优策略，PBO 应接近 `0`。
- 纯噪声策略组，PBO 必须落在 `[0, 1]` 且通常非零。
- walk-forward 切分必须训练在前、测试在后。
- Purged K-Fold 必须剔除测试折两侧 embargo 样本。
- 所有验证函数应为纯函数，离线确定性运行。
