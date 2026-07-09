import { ShieldCheck } from "lucide-react";

import type { RiskSnapshot } from "../../shared/api/types";
import { formatAmount, formatPercent, toNumber } from "../../shared/lib/format";
import { EmptyState } from "../../shared/ui/EmptyState";
import { Meter } from "../../shared/ui/Meter";
import { MetricRow } from "../../shared/ui/MetricRow";
import { Panel } from "../../shared/ui/Panel";

export function RiskPanel({ risk }: { risk: RiskSnapshot | null }) {
  if (!risk) {
    return (
      <Panel title="风控" icon={<ShieldCheck size={16} />}>
        <EmptyState>加载中</EmptyState>
      </Panel>
    );
  }

  return (
    <Panel title="风控" icon={<ShieldCheck size={16} />}>
      <div className="metric-stack">
        <MetricRow label="账户净值" value={formatAmount(risk.equity)} />
        <MetricRow
          label="日回撤"
          value={`${formatPercent(risk.drawdown.daily)} / ${formatPercent(risk.limits.daily_dd, 0)}`}
        />
        <Meter value={risk.drawdown.daily} max={risk.limits.daily_dd} />
        <MetricRow
          label="周回撤"
          value={`${formatPercent(risk.drawdown.weekly)} / ${formatPercent(risk.limits.weekly_dd, 0)}`}
        />
        <Meter value={risk.drawdown.weekly} max={risk.limits.weekly_dd} />
        <MetricRow
          label="总回撤"
          value={`${formatPercent(risk.drawdown.total)} / ${formatPercent(risk.limits.total_dd, 0)}`}
        />
        <Meter value={risk.drawdown.total} max={risk.limits.total_dd} />
        <MetricRow label="下单 / 小时" value={`${risk.orders_last_hour} / ${risk.limits.max_orders_per_hour}`} />
        <MetricRow label="连续亏损" value={`${risk.consecutive_losses} / ${risk.limits.max_consecutive_losses}`} />
        <MetricRow
          label="保证金健康度"
          value={
            risk.margin_ratio != null ? (
              <span
                className={
                  toNumber(risk.margin_ratio) < toNumber(risk.limits.min_margin_ratio ?? 0) * 2
                    ? "tone-short"
                    : "tone-long"
                }
              >
                {formatPercent(risk.margin_ratio)} / 下限 {formatPercent(risk.limits.min_margin_ratio ?? 0, 0)}
              </span>
            ) : (
              <span className="tone-muted">无永续仓</span>
            )
          }
        />
        <MetricRow
          label={`实盘校验 (${risk.mode})`}
          value={<span className={risk.live_guard.ok ? "tone-long" : "tone-short"}>{risk.live_guard.ok ? "通过" : "未通过"}</span>}
        />
        {risk.live_guard.reasons.length ? <p className="muted-line">{risk.live_guard.reasons.join("；")}</p> : null}
      </div>
    </Panel>
  );
}
