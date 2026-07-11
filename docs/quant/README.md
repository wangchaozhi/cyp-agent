# 量化规格分册

这里记录 Go 量化内核的公式、输入约束和测试标准。实现状态以代码和测试为准，研究计划不能被解释成已上线能力。

| 分册 | 内容 | 当前 Go 实现 |
| --- | --- | --- |
| [validation.md](validation.md) | Walk-forward、purged K-fold、embargo、PBO | `internal/backtest/validation.go` |
| [stats.md](stats.md) | Sharpe、PSR、Expected Max Sharpe、DSR、MinTRL | `internal/backtest/stats.go` |
| [risk.md](risk.md) | VaR/CVaR/压力测试规格 | 仅 CVaR 上限护栏已在 `internal/risk/engine.go` |
| [sizing.md](sizing.md) | EWMA、波动率目标、Kelly 边界 | EWMA 与策略入口已实现 |
| [portfolio.md](portfolio.md) | 协方差、MVO、HRP、ERC | 设计阶段 |
| [signals_execution.md](signals_execution.md) | Regime、协整、执行成本和调度 | 设计阶段 |

当前可运行入口：

```bash
go test ./internal/backtest ./internal/data ./internal/risk
go run ./cmd/cyp backtest --bars 300 --seed 7 --json
go run ./cmd/cyp sweep --bars 300 --top 5
```

共同约束：

- 时间序列按时间切分，不随机打乱，不读取未来数据。
- 报告记录 seed、参数、样本区间、成本假设和版本。
- 资金契约用精确 Decimal；统计数组使用有限 `float64`。
- 任何策略必须同时报告样本外表现、回撤和过拟合指标。
- 当前回测未包含真实交易成本，结果不构成上线许可。
