# 风控手册 · cyp-agent

> 这是与真金白银直接相关的文档。**风控引擎是确定性的、非 LLM 的、拥有一票否决权。**
> 铁律：智能负责找机会，护栏负责不出事。任何 LLM（含风控官）都**无权放宽**本文的硬护栏，只能收紧。

## 1. 三道防线

```
第一道：确定性风控引擎 risk.engine   ── 硬护栏，纯函数，一票否决（本文重点）
第二道：人工审批门 approval.gate      ── 人在环，每笔真实下单必经，超时=拒绝
第三道：Kill Switch + 冷静期          ── 熔断层，异常态一键/自动全面停手
```

三道防线**逐层收窄**：提案先过硬护栏，再过软评审，再到人工，任一环都可否决；运行期还有熔断兜底。

> 运行期的三条硬约束——**有仓必有保护、对账未完成不开新仓、护仓 fail-safe（超时自动保护，与开仓「超时=拒绝」方向相反）**——落地细节见 [RUNTIME.md](RUNTIME.md) §2/§3，其不变量已并入下方 §2.1 规则清单。

## 2. 硬护栏规则清单（`internal/risk/engine.go`）

每条规则一个纯函数、一个单测（含通过/否决边界）。默认阈值可在 `RiskConfig` 覆盖；实盘阈值应比模拟更保守。

### 2.1 通用（现货/合约/链上共用）

| 规则 | 默认阈值 | 违反行为 |
| --- | --- | --- |
| 单笔风险 R = 仓位×止损距离 ≤ 账户净值 × `max_risk_per_trade` | 1% | 否决 |
| **止损必填** | — | 无止损 → 否决 |
| **有仓必有保护**：入场成交后必存在交易所侧保护单（链上=监控覆盖），否则立即平掉裸仓 | — | 见 [RUNTIME.md §2](RUNTIME.md) |
| **对账未完成不开新仓**：`state=RECONCILING` 时拒绝新开仓，仅允许减仓/平仓 | — | 见 [RUNTIME.md §3](RUNTIME.md) |
| 单仓名义上限 ≤ 账户 × `max_position_pct` | 20% | 缩仓(downsized) |
| 总敞口上限 ≤ 账户 × `max_gross_exposure` | 100%（现货）/ 视保证金 | 否决 |
| 单标的集中度 ≤ `max_symbol_concentration` | 30% | 缩仓 |
| 相关性簇同向净敞口 ≤ `max_correlated_exposure`（跨场所聚合，majors/alt 聚类） | 50% | 缩仓/否决 |
| 组合 CVaR ≤ 账户净值 × `max_cvar_pct`（尾部损失护栏） | 3% | 否决 |
| 下单频率 ≤ `max_orders_per_window` | 10/小时 | 否决 |
| 预估滑点 ≤ `max_slippage_bps` | 30 bps | 否决 |
| Kill Switch 开启 | — | 全部否决 |

### 2.2 合约/永续专项

| 规则 | 默认阈值 | 违反行为 |
| --- | --- | --- |
| 杠杆上限 `max_leverage`（按品种） | 3x（起步保守） | 否决 |
| 单仓初始保证金预算 `max_margin_pct` | 净值的 10% | 超出时缩仓 |
| 动态爆仓缓冲 | 见下方数学模型 | 不安全时降杠杆；仍不可行则缩仓/否决 |
| 资金费拥挤度：资金费率极端且方向同向时收紧 | 可配 | 缩仓/否决 |
| 单账户维持保证金率 ≥ `min_margin_ratio` | 显式阈值 | 否决新开仓 |
| 全仓/逐仓模式校验 | 强制逐仓（默认） | 否决 |

#### 2.2.1 杠杆数学模型 `margin-volatility-v1`

仓位和杠杆分开计算：风险预算/分数 Kelly 先给出名义仓位 `N`，杠杆模型不放大 `N`，只计算该仓位需要的最低安全杠杆。

设账户权益为 `E`、止损距离占入场价比例为 `d`、EWMA 收益波动率为 `σ`：

```text
保证金预算       M = E × max_margin_pct
压力缓冲         B = max(min_liq_buffer,
                         liq_stop_multiple × d,
                         liq_vol_multiple × σ)
                     + liq_reserve_pct
安全杠杆上限     L_safe = floor_step(min(max_leverage, 1 / B))
安全名义仓位上限 N_max = M × L_safe
最终名义仓位     N_final = min(N, N_max)
最终杠杆         L = ceil_step(max(1, N_final / M))
预计初始保证金   margin = N_final / L
```

系统选择满足保证金预算的**最低**杠杆，而不是按信号置信度直接选 1/2/3 倍。若 `N / M` 大于安全上限，系统缩小 `N`，不会把杠杆硬顶到不安全区间。每次自动审批、人工改规模和反向再开仓后都会重新计算并重新 preflight。

默认示例：`E=10,000`、`N=2,000`、`d=5%`、`σ=2%` 时，`M=1,000`，`B=max(30%,10%,6%)+2%=32%`，`L_safe=3x`，满足预算的最低杠杆为 `2x`，预计保证金为 `1,000`。如果自动审批把名义仓位降至 `200`，最终杠杆同步降为 `1x`。

交易所真实强平价还受维持保证金档位、手续费、资金费与账户状态影响；`liq_reserve_pct` 是额外保守预留，交易所预检返回的爆仓距离仍必须大于模型压力缓冲。模型结果随 `TradeProposal.leverage_plan` 保存，便于审计。

#### 2.2.2 盈利递减加仓 `risk-pyramid-v1`

同向持仓不会无条件摊平亏损。只有当前仓位盈利达到 `add_min_profit_r` 且信号置信度过线时，才计算第 `k` 次加仓：

```text
第 k 次风险比例   r_k = max_risk_per_trade × add_risk_decay^k
风险仓位上限      N_risk = equity × r_k / stop_fraction
单次比例上限      N_tranche = existing_notional × add_max_position_fraction
最终加仓名义金额  N_add = min(strategy_notional, N_risk, N_tranche, 其他组合剩余额度)
```

默认 `add_risk_decay=0.5`、最多两次，因此第一/第二次加仓分别只使用基础风险预算的 50%/25%。每次加仓还必须满足 60 分钟冷却、单仓上限、相关簇上限、总敞口、CVaR、最终风险分和聚合保证金预算。逐仓合约按“现仓＋新增仓”的总名义金额重新计算杠杆，并以现有杠杆作为最低值，避免加仓时意外降低杠杆导致额外占用保证金。

### 2.3 链上 DeFi 专项（不可逆，风险最高）

| 规则 | 默认 | 违反行为 |
| --- | --- | --- |
| **禁止无限授权**：approve 额度 = 本次所需 | 精确额度 | 否决 |
| 合约白名单/蜜罐检查：目标合约须在白名单或通过安全检查 | 白名单 | 否决 |
| 最小流动性/池深：目标池 TVL ≥ `min_pool_tvl` | 可配 | 否决 |
| 价格冲击上限 ≤ `max_price_impact` | 1% | 否决 |
| gas 上限：gas price ≤ `max_gas_gwei` 且单笔 gas 成本 ≤ `max_gas_quote` | 20 USDT | 否决/等待 |
| MEV 防护：大额 swap 走私有内存池/防夹路由 | 强制 | 否决明文广播 |
| 跨桥禁止自动化 | — | 否决 |

## 3. 熔断与冷静期

| 触发 | 动作 |
| --- | --- |
| 日回撤 ≥ `daily_drawdown_limit`（默认 3%） | 冻结新开仓至次日，仅允许减仓/平仓 |
| 周回撤 ≥ `weekly_drawdown_limit`（默认 8%） | 冻结开仓 + 强制人工复核 |
| 总回撤 ≥ `max_drawdown_limit`（默认 15%） | 全面停手，进入只读/只减仓模式 |
| 连亏 ≥ `max_consecutive_losses`（默认 4 笔） | 拒绝新开仓；盈利平仓后计数重置 |
| LLM/数据异常率超阈 | 降级为纯规则 + 告警，暂停自动提案 |

风险状态由 `internal/riskstate` 写入当前配置的 Repository（memory/file/PostgreSQL）。日、周和总回撤、小时订单数、连续亏损与已实现盈亏重启后不会因进程内计数器清零；成交明细可通过 `GET /api/trades` 审计。

组合 CVaR 使用持久化净值变化的 95% 历史尾部均值估计，至少需要 20 个有效变化样本。样本不足时 API 明确返回 `null`，不会用 `0` 冒充已计算结果。

## 4. Kill Switch（一键停机）

- 触发方式：仪表盘按钮、`POST /api/killswitch` 或环境变量 `CYP_KILL=1`。
- 生效范围：**立即拒绝所有新提案与下单**；已挂订单按策略撤单；持仓保留（不强制平仓，避免踩踏，改为人工处置）。
- 恢复：需人工显式调用 `POST /api/killswitch` 设置 `on=false`；实盘执行仍由代码级硬门禁关闭。
- 不变量：Kill Switch 是硬护栏最外层，任何路径都无法绕过。

## 5. 密钥与私钥安全

| 类型 | 规则 |
| --- | --- |
| 交易所 API Key | 仅授「读 + 交易」权限，**永久禁用提现**；只从 env 读；绝不落盘/日志 |
| IP 白名单 | 交易所侧开启 API IP 白名单 |
| 链上私钥 | 走**隔离签名器**：本地加密 keystore / KMS / 硬件钱包；**永不落盘明文、永不进 LLM 上下文、永不出现在日志/事件/仪表盘** |
| 签名边界 | 签名器只接收结构化 `OrderIntent` 并本地签名，主进程拿不到私钥 |
| 日志脱敏 | `observability` 自动脱敏 `api_key/secret/private_key/mnemonic/token` 字段 |
| 权限分级 | READ/WRITE/EXECUTE；下单=EXECUTE 必经审批门 |

## 6. 模式与开关

| 配置 | 值 | 说明 |
| --- | --- | --- |
| `CYP_MODE` | `paper`（默认）/ `live` | `live` 需满足前置校验（Key 权限正确、Kill Switch 未触发、风控配置已审阅） |
| `CYP_APPROVAL` | `dashboard`（默认）/ `auto`（`cli` 为废弃别名） | `auto` 仅 M6，且受策略白名单 + 低 risk_score + 小额三重约束 |
| `RiskConfig.*` | 见 §2/§3 | 实盘阈值应显式设置且比默认更保守 |

尾部风险相关新增：

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `CYP_MAX_CVAR_PCT` | `0.03` | 组合 CVaR 尾部损失上限，占账户净值比例；超限拒绝新开仓 |

杠杆模型配置：

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `CYP_MAX_LEVERAGE` | `3` | 绝对杠杆上限 |
| `CYP_MAX_MARGIN_PCT` | `0.10` | 单仓最多占用的权益比例 |
| `CYP_LEVERAGE_STEP` | `1` | 杠杆离散步长，支持 `0.5` 等交易所规格 |
| `CYP_MIN_LIQ_BUFFER` | `0.30` | 最低爆仓距离 |
| `CYP_LIQ_STOP_MULTIPLE` | `2` | 爆仓距离至少为止损距离的倍数 |
| `CYP_LIQ_VOL_MULTIPLE` | `3` | 爆仓距离至少覆盖的 EWMA 波动倍数 |
| `CYP_LIQ_RESERVE_PCT` | `0.02` | 维持保证金、费用和模型误差预留 |

## 7. 风控相关的工程纪律

1. `internal/risk/engine.go` 每条规则**必须有单测**，含通过与否决边界；未测不合并。
2. 硬护栏是**纯函数**，不依赖 LLM、不做网络调用（数据由 preflight 预取）。
3. 任何放宽阈值的改动需在 PR 显式标注并复核。
4. `paper` 模式的端到端否决用例（如「无止损被拒」「超杠杆被拒」）是 CI 门禁。
5. 实盘特性上线前，必须在 `paper` 验证 Kill Switch 与至少一条熔断规则真实生效。

## 8. 免责

本项目为交易辅助工具，风控措施降低但**不消除**风险。加密货币交易（尤其合约与链上）可能导致本金全部损失。实盘由使用者自行决策并承担全部后果。
