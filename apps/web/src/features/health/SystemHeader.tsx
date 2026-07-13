import { Activity, BrainCircuit, Play, Power, RadioTower, Settings } from "lucide-react";

import type { HealthStatus, VenueInfo } from "../../shared/api/types";
import type { StreamStatus } from "../../shared/hooks/useEventStream";

interface SystemHeaderProps {
  health: HealthStatus | null;
  venues: VenueInfo[] | null;
  streamStatus: StreamStatus;
  running: boolean;
  switchingKill: boolean;
  settingsOpen: boolean;
  runDisabledReason: string | null;
  onRun: () => void;
  onToggleKill: () => void;
  onOpenSettings: () => void;
}

function streamLabel(status: StreamStatus): string {
  const labels: Record<StreamStatus, string> = {
    connecting: "连接中",
    open: "实时同步",
    reconnecting: "重新连接",
    closed: "连接断开",
  };
  return labels[status];
}

export function SystemHeader({
  health,
  venues,
  streamStatus,
  running,
  switchingKill,
  settingsOpen,
  runDisabledReason,
  onRun,
  onToggleKill,
  onOpenSettings,
}: SystemHeaderProps) {
  const configuredVenues = venues?.filter((venue) => venue.configured).length ?? 0;
  const totalVenues = venues?.length ?? 0;
  const killOn = Boolean(health?.kill);

  return (
    <header className="app-header">
      <div className="topbar-context">
        <div className="topbar-mobile-brand" aria-hidden="true">C</div>
        <div>
          <span>CONTROL CENTER</span>
          <h1>交易控制台</h1>
        </div>
      </div>

      <div className="status-strip" aria-label="系统状态">
        <span className="status-pill status-pill--mode">
          <Activity size={14} />
          <span>模式</span><b>{health?.display_mode ?? health?.mode ?? "--"}</b>
        </span>
        <span className={`status-pill status-pill--stream status-pill--${streamStatus === "open" ? "ok" : "warn"}`}>
          <RadioTower size={14} />
          <span>{streamLabel(streamStatus)}</span>
        </span>
        <span className="status-pill status-pill--analysis">
          <BrainCircuit size={14} />
          <span>{health?.llm ? "规则 + LLM" : "规则引擎"}</span>
        </span>
        <span className="status-pill status-pill--venues">
          场所 <b>{configuredVenues}/{totalVenues}</b>
        </span>
      </div>

      <div className="header-actions">
        <button
          className={`icon-command topbar-action ${settingsOpen ? "is-on" : ""}`}
          type="button"
          onClick={onOpenSettings}
          aria-expanded={settingsOpen}
          aria-controls="settings-drawer"
          title="系统设置"
        >
          <Settings size={17} />
          <span>设置</span>
        </button>
        <button
          className="command-button command-button--primary topbar-run"
          type="button"
          onClick={onRun}
          disabled={running || Boolean(runDisabledReason)}
          title={runDisabledReason ?? "运行一轮分析与决策"}
        >
          <Play size={16} fill="currentColor" />
          <span>{running ? "运行中" : runDisabledReason ? "暂不可用" : "运行分析"}</span>
        </button>
        <button
          className={`icon-command topbar-kill ${killOn ? "is-on" : ""}`}
          type="button"
          onClick={onToggleKill}
          disabled={switchingKill}
          title={killOn ? "解除安全停机" : "立即安全停机"}
        >
          <Power size={17} />
          <span>{killOn ? "解除停机" : "安全停机"}</span>
        </button>
      </div>
    </header>
  );
}
