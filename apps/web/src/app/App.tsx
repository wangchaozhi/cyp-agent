import { useCallback, useState } from "react";

import { PendingApprovals } from "../features/approvals/PendingApprovals";
import { BacktestPanel } from "../features/backtest/BacktestPanel";
import { EventStream } from "../features/events/EventStream";
import { SystemHeader } from "../features/health/SystemHeader";
import { OverviewStrip } from "../features/overview/OverviewStrip";
import { PortfolioPanel } from "../features/portfolio/PortfolioPanel";
import { PositionsPanel } from "../features/positions/PositionsPanel";
import { RiskPanel } from "../features/risk/RiskPanel";
import { cypApi } from "../shared/api/client";
import type { ApprovalRequest, DashboardEvent } from "../shared/api/types";
import { useEventStream } from "../shared/hooks/useEventStream";
import { usePollingResource } from "../shared/hooks/usePollingResource";

const MAX_EVENTS = 160;

type Notice = { tone: "ok" | "bad"; message: string } | null;

function refreshAll(resources: Array<() => Promise<void>>) {
  void Promise.allSettled(resources.map((refresh) => refresh()));
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "操作失败";
}

export default function App() {
  const health = usePollingResource(cypApi.health, 5000);
  const venues = usePollingResource(cypApi.venues, 10000);
  const pending = usePollingResource(cypApi.pending, 3000);
  const positions = usePollingResource(cypApi.positions, 5000);
  const risk = usePollingResource(cypApi.risk, 5000);
  const portfolio = usePollingResource(cypApi.portfolio, 5000);
  const [events, setEvents] = useState<DashboardEvent[]>([]);
  const [running, setRunning] = useState(false);
  const [switchingKill, setSwitchingKill] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);
  const apiError = [
    health.error,
    venues.error,
    pending.error,
    positions.error,
    risk.error,
    portfolio.error,
  ].find(Boolean);

  const handleEvent = useCallback(
    (event: DashboardEvent) => {
      setEvents((current) => [event, ...current].slice(0, MAX_EVENTS));

      if (["awaiting_approval", "approval_decided"].includes(event.type)) {
        refreshAll([pending.refresh]);
      }
      if (["run_started", "executed", "reviewed", "run_done"].includes(event.type)) {
        refreshAll([positions.refresh, risk.refresh, portfolio.refresh]);
      }
      if (["risk_assessed", "killswitch"].includes(event.type)) {
        refreshAll([health.refresh, risk.refresh]);
      }
    },
    [health.refresh, pending.refresh, portfolio.refresh, positions.refresh, risk.refresh],
  );

  const streamStatus = useEventStream(handleEvent);

  const runOnce = async () => {
    setRunning(true);
    setNotice(null);
    try {
      const result = await cypApi.runOnce();
      await pending.refresh();
      setNotice({ tone: "ok", message: `已触发 ${result.symbol}，run=${result.run_id}` });
    } catch (error) {
      setNotice({ tone: "bad", message: `触发失败：${errorMessage(error)}` });
    } finally {
      setRunning(false);
    }
  };

  const toggleKill = async () => {
    setSwitchingKill(true);
    setNotice(null);
    try {
      const next = !health.data?.kill;
      await cypApi.setKillSwitch(next);
      await Promise.all([health.refresh(), risk.refresh()]);
      setNotice({ tone: "ok", message: next ? "Kill Switch 已启用" : "Kill Switch 已解除" });
    } catch (error) {
      setNotice({ tone: "bad", message: `切换失败：${errorMessage(error)}` });
    } finally {
      setSwitchingKill(false);
    }
  };

  const decideApproval = async (runId: string, request: ApprovalRequest) => {
    setNotice(null);
    try {
      await cypApi.decideApproval(runId, request);
      await pending.refresh();
      setNotice({ tone: "ok", message: `审批已提交：${request.decision}` });
    } catch (error) {
      setNotice({ tone: "bad", message: `审批失败：${errorMessage(error)}` });
      throw error;
    }
  };

  return (
    <div className="app">
      <SystemHeader
        health={health.data}
        venues={venues.data}
        streamStatus={streamStatus}
        running={running}
        switchingKill={switchingKill}
        onRun={() => void runOnce()}
        onToggleKill={() => void toggleKill()}
      />

      {apiError ? <div className="app-alert">后端连接异常：{apiError}</div> : null}
      {notice ? <div className={`app-notice app-notice--${notice.tone}`}>{notice.message}</div> : null}

      <OverviewStrip
        health={health.data}
        pending={pending.data ?? []}
        positions={positions.data ?? []}
        risk={risk.data}
        portfolio={portfolio.data}
        streamStatus={streamStatus}
      />

      <main className="dashboard-grid">
        <PendingApprovals
          items={pending.data ?? []}
          loading={pending.loading}
          onDecide={decideApproval}
        />
        <EventStream events={events} status={streamStatus} />
        <PositionsPanel positions={positions.data ?? []} />
        <RiskPanel risk={risk.data} />
        <PortfolioPanel portfolio={portfolio.data} />
        <BacktestPanel />
      </main>
    </div>
  );
}
