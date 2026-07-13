import { Activity, CircleGauge, ClipboardCheck, TrendingDown, WalletCards } from "lucide-react";

import type {
  HealthStatus,
  MetricsSnapshot,
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
  metrics?: MetricsSnapshot | null;
  streamStatus: StreamStatus;
}

function riskTone(risk: RiskSnapshot | null): "ok" | "warn" | "bad" {
  if (!risk) return "warn";
  if (risk.kill || !risk.live_guard.ok) return "bad";
  const dailyRatio = toNumber(risk.limits.daily_dd) > 0
    ? toNumber(risk.drawdown.daily) / toNumber(risk.limits.daily_dd)
    : 0;
  const totalRatio = toNumber(risk.limits.total_dd) > 0
    ? toNumber(risk.drawdown.total) / toNumber(risk.limits.total_dd)
    : 0;
  return Math.max(dailyRatio, totalRatio) >= 0.65 ? "warn" : "ok";
}

function streamLabel(status: StreamStatus): string {
  const labels: Record<StreamStatus, string> = {
    connecting: "连接中",
    open: "实时同步",
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
  metrics,
  streamStatus,
}: OverviewStripProps) {
  const slo = metrics?.runs;
  const tone = riskTone(risk);
  const gross = portfolio ? formatAmount(portfolio.gross) : "--";
  const equity = risk ? formatAmount(risk.equity) : "--";
  const maxDrawdown = risk
    ? Math.max(toNumber(risk.drawdown.daily), toNumber(risk.drawdown.total))
    : 0;
  const safetyLabel = health?.kill
    ? "安全停机"
    : tone === "bad"
      ? "需要处理"
      : tone === "warn"
        ? "保持观察"
        : "风险受控";

  return (
    <section id="overview" className="overview-dashboard" aria-label="账户与系统总览">
      <article className={`overview-lead overview-lead--${tone}`}>
        <div className="overview-lead__top">
          <span>PORTFOLIO EQUITY</span>
          <b><i />{safetyLabel}</b>
        </div>
        <div className="overview-lead__value">
          <strong>{equity}</strong>
          <span>USDT</span>
        </div>
        <p>当前账户净值</p>
        <div className="overview-lead__footer">
          <span><Activity size={13} />{streamLabel(streamStatus)}</span>
          <span>{health?.display_mode ?? health?.mode ?? "--"}</span>
          <span>{health?.execution_venue?.toUpperCase() ?? "PAPER"}</span>
        </div>
      </article>

      <div className="overview-stats">
        <article className="overview-stat">
          <div className="overview-stat__icon overview-stat__icon--blue"><WalletCards size={18} /></div>
          <div>
            <span>持仓与总敞口</span>
            <strong>{positions.length}<small> 笔</small></strong>
            <p>{gross} USDT</p>
          </div>
        </article>

        <article className={`overview-stat ${pending.length ? "overview-stat--attention" : ""}`}>
          <div className="overview-stat__icon overview-stat__icon--amber"><ClipboardCheck size={18} /></div>
          <div>
            <span>等待人工决策</span>
            <strong>{pending.length}<small> 项</small></strong>
            <p>{pending.length ? "请检查提案与风险评估" : "当前没有待审批任务"}</p>
          </div>
        </article>

        <article className="overview-stat">
          <div className="overview-stat__icon overview-stat__icon--violet"><TrendingDown size={18} /></div>
          <div>
            <span>最大回撤</span>
            <strong>{formatPercent(maxDrawdown, 2)}</strong>
            <p>总限额 {risk ? formatPercent(risk.limits.total_dd, 0) : "--"}</p>
          </div>
        </article>

        <article className="overview-stat">
          <div className="overview-stat__icon overview-stat__icon--green"><CircleGauge size={18} /></div>
          <div>
            <span>执行质量 / 审批时延</span>
            <strong>{slo && slo.executed + slo.errors > 0 ? formatPercent(slo.order_success_rate, 0) : "--"}</strong>
            <p>{slo && slo.approval_latency.n > 0 ? `平均 ${slo.approval_latency.avg_s.toFixed(1)} 秒` : "等待有效样本"}</p>
          </div>
        </article>
      </div>
    </section>
  );
}
