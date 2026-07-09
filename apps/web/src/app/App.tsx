import { useCallback, useEffect, useState } from "react";
import { X } from "lucide-react";

import { PendingApprovals } from "../features/approvals/PendingApprovals";
import { BacktestPanel } from "../features/backtest/BacktestPanel";
import { EventStream } from "../features/events/EventStream";
import { SystemHeader } from "../features/health/SystemHeader";
import { MarketPanel } from "../features/market/MarketPanel";
import { OverviewStrip } from "../features/overview/OverviewStrip";
import { PortfolioPanel } from "../features/portfolio/PortfolioPanel";
import { PositionsPanel } from "../features/positions/PositionsPanel";
import { RiskPanel } from "../features/risk/RiskPanel";
import { SettingsPanel } from "../features/settings/SettingsPanel";
import { cypApi } from "../shared/api/client";
import type { ApprovalRequest, DashboardEvent, Position, RuntimeSettingsUpdate } from "../shared/api/types";
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
  const runtimeSettings = usePollingResource(cypApi.settings, 10000);
  const pending = usePollingResource(cypApi.pending, 3000);
  const positions = usePollingResource(cypApi.positions, 5000);
  const risk = usePollingResource(cypApi.risk, 5000);
  const portfolio = usePollingResource(cypApi.portfolio, 5000);
  const market = usePollingResource(() => cypApi.market(), 15000);
  const metrics = usePollingResource(cypApi.metrics, 10000);
  const [events, setEvents] = useState<DashboardEvent[]>([]);
  const [running, setRunning] = useState(false);
  const [switchingKill, setSwitchingKill] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);
  const apiError = [
    health.error,
    venues.error,
    runtimeSettings.error,
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
        refreshAll([health.refresh, risk.refresh, runtimeSettings.refresh]);
      }
    },
    [health.refresh, pending.refresh, portfolio.refresh, positions.refresh, risk.refresh, runtimeSettings.refresh],
  );

  const streamStatus = useEventStream(handleEvent);
  const runDisabledReason = health.data?.llm ? null : "LLM 未开启或模型 Key 未配置";

  useEffect(() => {
    if (!settingsOpen) return;

    function closeOnEscape(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setSettingsOpen(false);
      }
    }

    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [settingsOpen]);

  const runOnce = async () => {
    if (runDisabledReason) {
      setNotice({ tone: "bad", message: `无法触发：${runDisabledReason}` });
      return;
    }

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
      await Promise.all([health.refresh(), risk.refresh(), runtimeSettings.refresh()]);
      setNotice({ tone: "ok", message: next ? "Kill Switch 已启用" : "Kill Switch 已解除" });
    } catch (error) {
      setNotice({ tone: "bad", message: `切换失败：${errorMessage(error)}` });
    } finally {
      setSwitchingKill(false);
    }
  };

  const saveSettings = async (payload: RuntimeSettingsUpdate) => {
    setNotice(null);
    try {
      await cypApi.updateSettings(payload);
      await Promise.all([runtimeSettings.refresh(), health.refresh()]);
      setNotice({ tone: "ok", message: "设置已保存，LLM 配置已更新" });
    } catch (error) {
      setNotice({ tone: "bad", message: `保存设置失败：${errorMessage(error)}` });
      throw error;
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

  const closePosition = async (position: Position) => {
    setNotice(null);
    try {
      const result = await cypApi.closePosition({
        symbol: position.symbol,
        instrument: position.instrument,
      });
      await Promise.all([positions.refresh(), risk.refresh(), portfolio.refresh()]);
      setNotice({ tone: "ok", message: `已平仓 ${position.symbol}，均价=${result.avg_price ?? "-"}` });
    } catch (error) {
      setNotice({ tone: "bad", message: `平仓失败：${errorMessage(error)}` });
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
        settingsOpen={settingsOpen}
        runDisabledReason={runDisabledReason}
        onRun={() => void runOnce()}
        onToggleKill={() => void toggleKill()}
        onOpenSettings={() => setSettingsOpen(true)}
      />

      {settingsOpen ? (
        <div className="settings-overlay" role="presentation" onClick={() => setSettingsOpen(false)}>
          <aside
            id="settings-drawer"
            className="settings-drawer"
            role="dialog"
            aria-modal="true"
            aria-label="设置"
            onClick={(event) => event.stopPropagation()}
          >
            <button
              className="settings-drawer__close icon-command"
              type="button"
              onClick={() => setSettingsOpen(false)}
              aria-label="关闭设置"
              title="关闭设置"
            >
              <X size={16} />
              <span>关闭</span>
            </button>
            <SettingsPanel
              settings={runtimeSettings.data}
              venues={venues.data ?? []}
              onSave={saveSettings}
            />
          </aside>
        </div>
      ) : null}

      {apiError ? <div className="app-alert">后端连接异常：{apiError}</div> : null}
      {notice ? <div className={`app-notice app-notice--${notice.tone}`}>{notice.message}</div> : null}

      <OverviewStrip
        health={health.data}
        pending={pending.data ?? []}
        positions={positions.data ?? []}
        risk={risk.data}
        portfolio={portfolio.data}
        metrics={metrics.data}
        streamStatus={streamStatus}
      />

      <main className="dashboard-stack">
        <section className="dashboard-hero" aria-label="运行工作区">
          <PendingApprovals
            items={pending.data ?? []}
            loading={pending.loading}
            onDecide={decideApproval}
          />
          <EventStream events={events} status={streamStatus} />
        </section>

        <section className="dashboard-metrics" aria-label="账户与风险概览">
          <PositionsPanel positions={positions.data ?? []} onClose={closePosition} />
          <RiskPanel risk={risk.data} />
          <PortfolioPanel portfolio={portfolio.data} />
          <MarketPanel market={market.data} />
        </section>

        <BacktestPanel />
      </main>
    </div>
  );
}
