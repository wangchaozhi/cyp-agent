# 量化内核

cyp-agent 的量化层服务于两个目标：让回测可复现，以及让风险决策可验证。当前实现全部位于 Go 包中，不依赖重型数值计算库；任何统计结果都只是筛选证据，不能越过确定性风控、审批门或 v0.2.0 的真实执行硬禁令。

## 当前实现

| 能力 | Go 路径 | 状态 |
| --- | --- | --- |
| 合成价格、蜡烛回放、权益曲线和交易记录 | `internal/backtest/backtest.go` | ✅ |
| 策略网格、排序、OOS 与稳健结论 | `internal/backtest/optimize.go` | ✅ |
| Sharpe、PSR、Expected Max Sharpe、DSR、MinTRL | `internal/backtest/stats.go` | ✅ |
| Walk-forward、purged K-fold、embargo、PBO | `internal/backtest/validation.go` | ✅ |
| EWMA 与 realized volatility | `internal/data/volatility.go` | ✅ |
| 波动率/ATR 止损、波动率目标入口 | `internal/agents/strategist.go` | ✅ |
| 组合 CVaR 上限护栏 | `internal/risk/engine.go` | ✅ 接受上游 CVaR 值 |
| PostgreSQL OHLCV 异步归档、保留与缺口补录 | `internal/ohlcv` | ✅ |
| 手续费/点差/线性滑点/资金费成本 | `internal/backtest` | ✅ |
| 深度相关非线性冲击成本 | — | ⏳ |
| Bootstrap 置信区间 | — | ⏳ |
| 实测协方差与 HRP/ERC | — | ⏳ |

统计和验证的详细定义见 [quant/stats.md](quant/stats.md) 与 [quant/validation.md](quant/validation.md)。

## 回测入口

```bash
# 确定性合成数据；相同 seed 和参数应产生相同 JSON
go run ./cmd/cyp backtest \
  --symbol BTC/USDT \
  --bars 300 \
  --window 60 \
  --seed 7 \
  --drift 0.001 \
  --vol 0.01 \
  --json

# 27 组默认网格，输出前 5 名以及 OOS/PBO/DSR 结论
go run ./cmd/cyp sweep --symbol BTC/USDT --bars 300 --top 5
```

CLI 的 `backtest` 和 `sweep` 只使用合成数据。服务端 `POST /api/backtest` 可以分页抓取 CEX 历史 K 线，并自动复用 PostgreSQL 归档。`RunCandles` 本身保持纯计算且不执行交易。

## OHLCV 数据闭环

`CYP_OHLCV_ARCHIVE_ENABLED=true` 时，实时 CEX 快照经有界队列异步写入 PostgreSQL，数据库延迟不会进入交易关键路径。归档仅接受已收盘、价格为正、`low <= open/close <= high` 且成交量非负的数据；唯一键为 `(venue, symbol, timeframe, ts)`，重复采集使用 upsert。

默认保留 730 天 1 小时 K 线。应用启动后立即、此后每 6 小时比较保留窗口和已存时间点，按缺口调用交易所时间范围分页接口补录，因此计划停机和意外中断不会永久制造空洞。补录同样经过闭合与质量校验；交易所不可用时保留缺口供下一轮重试。行情曲线优先刷新上游并合并归档，上游临时失败时只有归档深度满足请求才会降级读取缓存。

## 基础回测模型

当前迁移验收策略是移动均线偏离阈值的单向 Paper 策略：

\[
d_t = \frac{P_t}{\operatorname{mean}(P_{t-w:t})} - 1
\]

当 \(d_t > \theta\sigma\) 时持有多仓；信号消失或价格触发波动倍数止损/止盈时退出。它的目的在于验证数据、参数、报告和稳健性管线，不代表推荐策略。

报告包含：

- 期初/期末权益、总收益和最大回撤；
- 单周期 Sharpe、交易数、胜率和盈亏因子；
- 逐笔交易、权益曲线、参数和经验说明。

当前基础引擎已计入可配置的双边手续费、半点差、线性滑点和逐周期资金费，并在报告中单列总成本与逐笔成本。尚未按订单规模和盘口深度标定非线性冲击，因此仍不得用绝对收益作为上线依据。

## 扫参与稳健性

`Grid` 对入场阈值、止损波动倍数和止盈波动倍数做笛卡尔积。默认目标函数为：

\[
\text{score}=\text{total return}-\text{maximum drawdown}
\]

`RobustSweep` 的当前流程：

1. 按时间把前 70% 数据作为样本内区间。
2. 在样本内选择默认目标函数最高的配置。
3. 计算候选策略收益矩阵的 PBO。
4. 在独立种子的后 30% 区间评价最佳配置。
5. 用所有候选的样本内 Sharpe 校正最佳配置的样本外 Sharpe，得到 DSR。

默认通过条件是 `PBO <= 0.5`、样本外收益为正且 `DSR >= 0.5`；否则返回 `REJECT(疑似过拟合)`。这个结论是研究门，不是执行许可。

## 波动率

`internal/data/volatility.go` 提供简单收益、EWMA 波动率和 realized volatility：

\[
r_t = \frac{P_t}{P_{t-1}}-1
\]

\[
\sigma_t^2 = \lambda\sigma_{t-1}^2 + (1-\lambda)r_t^2
\]

默认 \(\lambda=0.94\)。策略官可用 EWMA 波动构造止损距离，或以 `VolTarget / EWMA` 缩放仓位；最终仓位仍受 `internal/risk` 的单笔风险、仓位和敞口上限裁剪。

## 风险测度边界

`risk.ruleCVAR` 当前只消费 `RiskContext.PortfolioCVARQuote`：若上游提供的 CVaR 超过 `equity × MaxCVARPct`，新仓被拒绝；为空时不会自行估算。历史 VaR/CVaR 计算、收益窗口质量检查和压力场景尚未进入 Go 主链，不能在文档或界面中声称已有完整尾部风险模型。

## 数据纪律

- 蜡烛必须按时间升序，价格为正，时间戳和 symbol/timeframe 一致。
- 时间序列切分禁止随机打乱；所有特征只读取当时可见的数据。
- 任何调参试验数都必须进入 DSR/PBO 解释，不能只展示最佳组合。
- CEX 原始历史应保留 venue、symbol、timeframe、timestamp 和抓取时间。
- Decimal 用于资金和契约；统计计算可用 `float64`，但必须拒绝 NaN/Inf 并在返回 API 前有限化。
- 研究报告必须记录 seed、参数、样本区间、成本假设和代码版本。

## 测试要求

对应测试集中在 `internal/backtest/*_test.go` 和 `internal/data/data_test.go`：

1. 固定 seed 的回测 JSON 可复现且无 NaN/Inf。
2. 参数边界与 HTTP 请求边界一致。
3. Walk-forward 与 purged split 不重叠且 embargo 生效。
4. 正态 CDF/PPF 互逆误差、PSR/DSR/MinTRL 边界可解释。
5. 构造过拟合策略矩阵时 PBO 明显升高。
6. 增加真实成本模型后，零成本必须退化为当前结果，成本增加不得改善净收益。

## 后续优先级

### Q2：真实可用的回测

- 手续费、bid/ask、资金费和平方根冲击成本。
- Block/bootstrap 收益与最大回撤置信区间。
- 按波动/趋势/流动性 regime 分层报告。
- OHLCV 缺口、重复、异常值和时区质量报告。

### Q3：组合与执行

- EWMA/收缩协方差与相关性置信度。
- HRP/ERC 目标权重，并由现有风控作最终裁剪。
- TWAP/VWAP 调度与成交质量基准。

### Q4：模型治理

- 三重障碍标注、meta-labeling 与可解释特征。
- 数据版本、试验登记、champion/challenger 和退役规则。
- 对所有外部数值依赖做许可证、供应链和可复现构建审查。
