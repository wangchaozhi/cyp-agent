import { useCallback, useState } from "react";

import { PendingApprovals } from "../features/approvals/PendingApprovals";
import { EventStream } from "../features/events/EventStream";
import { SystemHeader } from "../features/health/SystemHeader";
import { PortfolioPanel } from "../features/portfolio/PortfolioPanel";
import { PositionsPanel } from "../features/positions/PositionsPanel";
import { RiskPanel } from "../features/risk/RiskPanel";
import { cypApi } from "../shared/api/client";
import type { ApprovalRequest, DashboardEvent } from "../shared/api/types";
import { useEventStream } from "../shared/hooks/useEventStream";
import { usePollingResource } from "../shared/hooks/usePollingResource";

const MAX_EVENTS = 160;

function refreshAll(resources: Array<() => Promise<void>>) {
  void Promise.allSettled(resources.map((refresh) => refresh()));
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
    try {
      await cypApi.runOnce();
      await pending.refresh();
    } finally {
      setRunning(false);
    }
  };

  const toggleKill = async () => {
    setSwitchingKill(true);
    try {
      await cypApi.setKillSwitch(!health.data?.kill);
      await Promise.all([health.refresh(), risk.refresh()]);
    } finally {
      setSwitchingKill(false);
    }
  };

  const decideApproval = async (runId: string, request: ApprovalRequest) => {
    await cypApi.decideApproval(runId, request);
    await pending.refresh();
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
      </main>
    </div>
  );
}
