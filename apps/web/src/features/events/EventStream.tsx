import { ListTree } from "lucide-react";

import type { DashboardEvent } from "../../shared/api/types";
import type { StreamStatus } from "../../shared/hooks/useEventStream";
import {
  formatAmount,
  formatClock,
  formatConfidence,
  sideLabel,
  verdictLabel,
} from "../../shared/lib/format";
import { EmptyState } from "../../shared/ui/EmptyState";
import { Panel } from "../../shared/ui/Panel";

interface EventStreamProps {
  events: DashboardEvent[];
  status: StreamStatus;
}

const LABELS: Record<string, string> = {
  run_started: "开始",
  snapshot_ready: "采集",
  reports_ready: "分析",
  proposal_ready: "提案",
  risk_assessed: "风控",
  awaiting_approval: "待审批",
  approval_decided: "审批",
  executed: "执行",
  reviewed: "复盘",
  run_done: "完成",
  run_failed: "失败",
  killswitch: "停机",
  position_monitor: "监控",
  reconciled: "对账",
  automation_evaluated: "退出观察",
  automated_exit: "自动平仓",
  reversal_observed: "反向确认",
  reversal_closed: "反向平仓",
  reversal_reassessed: "反向风控",
  reversal_opened: "反向开仓",
};

const RUN_STATUS_LABELS: Record<string, string> = {
  executed: "成交完成",
  rejected: "风控拒绝",
  not_approved: "未获审批",
  no_trade: "观望完成",
  execution_failed: "执行失败",
  error: "运行错误",
};

export function summarizeEvent(event: DashboardEvent): string {
  if (event.type === "reports_ready") {
    const reports = event.reports ?? [];
    const coverage = reports.filter((report) => !report.degraded).length;
    const detail = reports
        ?.map((report) => {
          if (report.degraded) {
            return `${report.agent}:无数据`;
          }
          return `${report.agent}:${report.stance}(强度=${formatConfidence(report.confidence)})`;
        })
        .join("  ") || "分析完成";
    return `覆盖=${coverage}/${reports.length}  ${detail}`;
  }

  if (event.type === "proposal_ready" && event.proposal) {
    const proposal = event.proposal;
    if (proposal.side === "flat") {
      return `观望 ${proposal.symbol} 强度=${formatConfidence(proposal.confidence)} 原因=${proposal.thesis}`;
    }
    const referencePrice = proposal.entry.price ?? proposal.entry.low ?? proposal.entry.high ?? "-";
    return `${sideLabel(proposal.side)} ${proposal.symbol} 名义金额=${formatAmount(proposal.size_quote)} USDT 参考价=${referencePrice} 止损价=${proposal.stop_loss ?? "-"} 强度=${formatConfidence(proposal.confidence)}`;
  }

  if (event.type === "risk_assessed" && event.assessment) {
    const assessment = event.assessment;
    const violations = assessment.hard_violations.length
      ? ` 护栏=${assessment.hard_violations.join("; ")}`
      : "";
    return `${verdictLabel(assessment.verdict)} risk=${formatConfidence(assessment.risk_score)}${violations}`;
  }

  if (event.type === "executed" && event.execution) {
    const execution = event.execution;
    return `${execution.status} 均价=${execution.avg_price ?? "-"} 保护单=${execution.protective_orders.length}`;
  }

  if (event.type === "reviewed" && event.review) {
    const prefix = event.review.kind === "close" ? `平仓复盘 PnL=${event.review.pnl_quote}` : "入场检查";
    const lessons = event.review.lessons.join(" / ");
    return lessons ? `${prefix} ${lessons}` : prefix;
  }

  if (event.type === "automation_evaluated" && event.exit_decision) {
    const decision = event.exit_decision;
    return `${event.symbol ?? "-"} ${decision.reason} 当前=${decision.current_r.toFixed(2)}R 峰值=${decision.peak_r.toFixed(2)}R 确认=${decision.confirmations}`;
  }

  if (event.type === "automated_exit" && event.exit_decision) {
    const decision = event.exit_decision;
    return `${event.symbol ?? "-"} ${decision.reason} 成交均价=${event.execution?.avg_price ?? "-"}`;
  }

  if (event.type === "reversal_observed" && event.reversal) {
    return `${event.symbol ?? "-"} ${sideLabel(event.position_side ?? "flat")} → ${sideLabel(event.proposal_side ?? event.reversal.side)} ${event.reversal.reason} 确认=${event.reversal.confirmations}/${event.reversal.required}`;
  }

  if (event.type === "reversal_closed" && event.execution) {
    return `${event.symbol ?? "-"} 旧${sideLabel(event.side ?? "flat")}仓已平，均价=${event.execution.avg_price ?? "-"}`;
  }

  if (event.type === "reversal_reassessed" && event.assessment) {
    return `${event.symbol ?? "-"} 重新风控：${verdictLabel(event.assessment.verdict)} risk=${formatConfidence(event.assessment.risk_score)}`;
  }

  if (event.type === "reversal_opened" && event.execution) {
    return `${event.symbol ?? "-"} 新${sideLabel(event.side ?? "flat")}仓 ${event.execution.status} 均价=${event.execution.avg_price ?? "-"}`;
  }

  if (event.type === "awaiting_approval" && event.proposal) {
    return `${sideLabel(event.proposal.side)} ${event.symbol ?? event.proposal.symbol} 等待人工确认`;
  }

  if (event.type === "snapshot_ready") {
    return `${event.symbol ?? "-"} ${event.bars ?? 0} 根 K 线`;
  }

  if (event.type === "approval_decided" && event.decision) {
    return `${event.decision.operator}: ${event.decision.decision} ${event.decision.note}`;
  }

  if (event.type === "run_done") {
    const status = event.status ?? "done";
    return `${event.symbol ?? "-"} ${RUN_STATUS_LABELS[status] ?? status}`;
  }

  if (event.type === "run_failed") {
    return event.error || "运行失败";
  }

  if (event.type === "killswitch") {
    return event.on ? "Kill Switch 已启用" : "Kill Switch 已解除";
  }

  if (event.type === "position_monitor") {
    return "持仓监控更新";
  }

  if (event.type === "reconciled") {
    return "对账完成";
  }

  if (event.type === "run_started") {
    return `${event.symbol ?? "-"} run=${event.run_id}`;
  }

  return event.symbol ?? "";
}

export function eventTone(event: DashboardEvent): string {
  if (event.type === "run_done") {
    if (["error", "execution_failed"].includes(event.status ?? "")) return "event-row--bad";
    if (["rejected", "not_approved", "no_trade"].includes(event.status ?? "")) return "event-row--warn";
    return event.status === "executed" ? "event-row--ok" : "";
  }
  if (event.type === "run_failed") return "event-row--bad";
  if (["risk_assessed", "awaiting_approval", "killswitch"].includes(event.type)) return "event-row--warn";
  if (["executed", "reviewed", "automated_exit", "reversal_closed", "reversal_opened"].includes(event.type)) return "event-row--ok";
  if (["automation_evaluated", "reversal_observed", "reversal_reassessed"].includes(event.type)) return "event-row--warn";
  return "";
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

export function EventStream({ events, status }: EventStreamProps) {
  return (
    <Panel
      className="panel--events"
      title="运行时间线"
      icon={<ListTree size={16} />}
      actions={<span className={`stream-badge stream-badge--${status}`}>{streamLabel(status)}</span>}
    >
      {events.length ? (
        <div className="event-list">
          {events.map((event, index) => (
            <article className={`event-row ${eventTone(event)}`} key={`${event.ts}-${event.type}-${index}`}>
              <time>{formatClock(event.ts)}</time>
              <span className="event-row__label">{LABELS[event.type] ?? event.type}</span>
              <span className="event-row__summary">{summarizeEvent(event)}</span>
            </article>
          ))}
        </div>
      ) : (
        <EmptyState>等待事件</EmptyState>
      )}
    </Panel>
  );
}
