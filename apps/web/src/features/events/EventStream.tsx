import { ListTree } from "lucide-react";

import type { DashboardEvent } from "../../shared/api/types";
import type { StreamStatus } from "../../shared/hooks/useEventStream";
import {
  formatAmount,
  formatClock,
  formatConfidence,
  formatPercent,
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
  proposal_ready: "决策",
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
};

function summarize(event: DashboardEvent): string {
  if (event.type === "reports_ready") {
    return (
      event.reports
        ?.map((report) => {
          const flag = report.degraded ? "*" : "";
          return `${report.agent}:${report.stance}(${formatConfidence(report.confidence)})${flag}`;
        })
        .join("  ") || "分析完成"
    );
  }

  if (event.type === "proposal_ready" && event.proposal) {
    const proposal = event.proposal;
    return `${sideLabel(proposal.side)} ${proposal.symbol} 规模=${formatAmount(proposal.size_quote)} 止损=${proposal.stop_loss ?? "-"} 置信=${formatConfidence(proposal.confidence)}`;
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
    return event.review.lessons.join(" / ") || "复盘完成";
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
    return `${event.symbol ?? "-"} ${event.status ?? "done"}`;
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

function eventTone(type: string): string {
  if (["run_failed"].includes(type)) return "event-row--bad";
  if (["risk_assessed", "awaiting_approval", "killswitch"].includes(type)) return "event-row--warn";
  if (["executed", "reviewed", "run_done"].includes(type)) return "event-row--ok";
  return "";
}

export function EventStream({ events, status }: EventStreamProps) {
  return (
    <Panel
      className="panel--events"
      title="事件流"
      icon={<ListTree size={16} />}
      actions={<span className={`stream-badge stream-badge--${status}`}>{status}</span>}
    >
      {events.length ? (
        <div className="event-list">
          {events.map((event, index) => (
            <article className={`event-row ${eventTone(event.type)}`} key={`${event.ts}-${event.type}-${index}`}>
              <time>{formatClock(event.ts)}</time>
              <span className="event-row__label">{LABELS[event.type] ?? event.type}</span>
              <span className="event-row__summary">{summarize(event)}</span>
            </article>
          ))}
        </div>
      ) : (
        <EmptyState>等待事件</EmptyState>
      )}
    </Panel>
  );
}
