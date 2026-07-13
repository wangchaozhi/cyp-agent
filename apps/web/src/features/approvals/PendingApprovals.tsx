import { Check, Pencil, ShieldAlert, X } from "lucide-react";
import { useState } from "react";

import type { ApprovalRequest, PendingApproval } from "../../shared/api/types";
import {
  formatAmount,
  formatConfidence,
  sideClass,
  sideLabel,
  verdictLabel,
} from "../../shared/lib/format";
import { EmptyState } from "../../shared/ui/EmptyState";
import { MetricRow } from "../../shared/ui/MetricRow";
import { Panel } from "../../shared/ui/Panel";

interface PendingApprovalsProps {
  items: PendingApproval[];
  loading: boolean;
  onDecide: (runId: string, request: ApprovalRequest) => Promise<void>;
}

export function PendingApprovals({ items, loading, onDecide }: PendingApprovalsProps) {
  const [sizes, setSizes] = useState<Record<string, string>>({});
  const [busyRun, setBusyRun] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function decide(runId: string, request: ApprovalRequest) {
    setBusyRun(runId);
    setError(null);
    try {
      await onDecide(runId, request);
      if (request.decision === "modify") {
        setSizes((current) => ({ ...current, [runId]: "" }));
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "审批提交失败");
    } finally {
      setBusyRun(null);
    }
  }

  return (
    <Panel title="审批队列" icon={<ShieldAlert size={16} />}>
      {error ? <div className="inline-alert">{error}</div> : null}
      {items.length ? (
        <div className="approval-list">
          {items.map(({ run_id, proposal, assessment }) => {
            const nextSize = sizes[run_id] ?? "";
            const parsedSize = Number(nextSize);
            const canModify = Number.isFinite(parsedSize) && parsedSize > 0;
            const effectiveSize = assessment.adjusted_size_quote || proposal.size_quote;
            const disabled = busyRun === run_id;

            return (
              <article className="approval-card" key={run_id}>
                <div className="approval-card__top">
                  <div className="instrument-line">
                    <span className={`side-chip ${sideClass(proposal.side)}`}>
                      {sideLabel(proposal.side)}
                    </span>
                    <strong>{proposal.symbol}</strong>
                    <span>{proposal.venue} · {proposal.instrument}</span>
                  </div>
                  <span className={`verdict verdict--${assessment.verdict}`}>
                    {verdictLabel(assessment.verdict)} · risk {formatConfidence(assessment.risk_score)}
                  </span>
                </div>

                <div className="metric-stack">
                  <MetricRow
                    label="规模"
                    value={`${formatAmount(effectiveSize)}${assessment.adjusted_size_quote ? " (降档)" : ""}`}
                  />
                  <MetricRow
                    label="止损 / 止盈"
                    value={`${proposal.stop_loss ?? "-"} / ${proposal.take_profit.join(", ") || "-"}`}
                  />
                  <MetricRow label="杠杆 / 置信" value={`${proposal.leverage}x / ${formatConfidence(proposal.confidence)}`} />
                  {proposal.leverage_plan ? (
                    <>
                      <MetricRow
                        label="杠杆模型"
                        value={`${proposal.leverage_plan.selected_leverage}x（所需 ${proposal.leverage_plan.required_leverage}x / 安全上限 ${proposal.leverage_plan.safe_max_leverage}x）`}
                      />
                      <MetricRow
                        label="保证金预算"
                        value={`${formatAmount(proposal.leverage_plan.estimated_margin_quote)} / ${formatAmount(proposal.leverage_plan.margin_budget_quote)}`}
                      />
                      <MetricRow
                        label="压力缓冲"
                        value={formatConfidence(Number(proposal.leverage_plan.required_liquidation_buffer))}
                      />
                    </>
                  ) : null}
                  {proposal.add_on_plan ? (
                    <>
                      <MetricRow
                        label="自动加仓"
                        value={`${proposal.add_on_plan.add_index}/${proposal.add_on_plan.max_adds} · 浮盈 ${proposal.add_on_plan.profit_r.toFixed(2)}R`}
                      />
                      <MetricRow
                        label="加仓风险预算"
                        value={`${formatConfidence(proposal.add_on_plan.risk_fraction)} · ${formatAmount(proposal.add_on_plan.recommended_notional_quote)} USDT`}
                      />
                    </>
                  ) : null}
                  {assessment.hard_violations.length ? (
                    <MetricRow label="护栏" value={assessment.hard_violations.join("; ")} />
                  ) : null}
                </div>

                {proposal.thesis ? <p className="approval-card__thesis">{proposal.thesis}</p> : null}

                <div className="approval-actions">
                  <button
                    className="icon-command icon-command--ok"
                    type="button"
                    disabled={disabled}
                    onClick={() => void decide(run_id, { decision: "approve" })}
                    title="批准"
                  >
                    <Check size={16} />
                    <span>{disabled ? "提交中" : "批准"}</span>
                  </button>
                  <button
                    className="icon-command icon-command--danger"
                    type="button"
                    disabled={disabled}
                    onClick={() => void decide(run_id, { decision: "reject" })}
                    title="拒绝"
                  >
                    <X size={16} />
                    <span>{disabled ? "提交中" : "拒绝"}</span>
                  </button>
                  <input
                    type="number"
                    min="0"
                    step="0.01"
                    value={nextSize}
                    placeholder="新规模"
                    aria-label={`${proposal.symbol} 新规模`}
                    onChange={(event) => setSizes((current) => ({ ...current, [run_id]: event.target.value }))}
                  />
                  <button
                    className="icon-command"
                    type="button"
                    disabled={disabled || !canModify}
                    onClick={() => void decide(run_id, { decision: "modify", size: parsedSize })}
                    title="修改规模"
                  >
                    <Pencil size={16} />
                    <span>改规模</span>
                  </button>
                </div>
              </article>
            );
          })}
        </div>
      ) : (
        <EmptyState>{loading ? "加载中" : "暂无待审批"}</EmptyState>
      )}
    </Panel>
  );
}
