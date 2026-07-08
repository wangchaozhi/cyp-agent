import { AlertTriangle, CircleDollarSign, Shield, TimerReset, WalletCards } from "lucide-react";

import type {
  HealthStatus,
  PendingApproval,
  PortfolioSnapshot,
  Position,
  RiskSnapshot,
} from "../../shared/api/types";
import type { StreamStatus } from "../../shared/hooks/useEventStream";
import { formatAmount, formatPercent, toNumber } from "../../shared/lib/format";

interface OverviewStripProps {
  health: HealthStatus | null;
  pending: PendingApproval[];
  positions: Position[];
  risk: RiskSnapshot | null;
  portfolio: PortfolioSnapshot | null;
  streamStatus: StreamStatus;
}

function riskTone(risk: RiskSnapshot | null): "ok" | "warn" | "bad" {
  if (!risk) return "warn";
  if (risk.kill || !risk.live_guard.ok) return "bad";

  const daily = toNumber(risk.drawdown.daily);
  const dailyLimit = toNumber(risk.limits.daily_dd);
  const total = toNumber(risk.drawdown.total);
  const totalLimit = toNumber(risk.limits.total_dd);
  const dailyRatio = dailyLimit > 0 ? daily / dailyLimit : 0;
  const totalRatio = totalLimit > 0 ? total / totalLimit : 0;

  return Math.max(dailyRatio, totalRatio) >= 0.65 ? "warn" : "ok";
}

function streamLabel(status: StreamStatus): string {
  const labels: Record<StreamStatus, string> = {
    connecting: "连接中",
    open: "实时",
    reconnecting: "重连中",
    closed: "已断开",
  };
  return labels[status];
}

export function OverviewStrip({
  health,
  pending,
  positions,
  risk,
  portfolio,
  streamStatus,
}: OverviewStripProps) {
  const tone = riskTone(risk);
  const gross = portfolio ? formatAmount(portfolio.gross) : "--";
  const equity = risk ? formatAmount(risk.equity) : "--";
  const maxDrawdown = risk
    ? Math.max(toNumber(risk.drawdown.daily), toNumber(risk.drawdown.total))
    : 0;

  return (
    <section className="overview-strip" aria-label="仪表盘总览">
      <article className={`overview-card overview-card--${tone}`}>
        <Shield size={18} />
        <div>
          <span>安全状态</span>
          <strong>{health?.kill ? "停机保护" : tone === "bad" ? "需处理" : tone === "warn" ? "观察" : "正常"}</strong>
        </div>
      </article>

      <article className="overview-card">
        <CircleDollarSign size={18} />
        <div>
          <span>账户净值</span>
          <strong>{equity}</strong>
        </div>
      </article>

      <article className="overview-card">
        <WalletCards size={18} />
        <div>
          <span>持仓 / 敞口</span>
          <strong>{positions.length} / {gross}</strong>
        </div>
      </article>

      <article className={`overview-card ${pending.length ? "overview-card--warn" : ""}`}>
        <AlertTriangle size={18} />
        <div>
          <span>待审批</span>
          <strong>{pending.length}</strong>
        </div>
      </article>

      <article className={`overview-card overview-card--${streamStatus === "open" ? "ok" : "warn"}`}>
        <TimerReset size={18} />
        <div>
          <span>SSE / 最大回撤</span>
          <strong>{streamLabel(streamStatus)} / {formatPercent(maxDrawdown)}</strong>
        </div>
      </article>
    </section>
  );
}
