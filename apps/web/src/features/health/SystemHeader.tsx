import { Activity, Play, Power, RadioTower, Settings } from "lucide-react";

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
    open: "实时",
    reconnecting: "重连中",
    closed: "已断开",
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
      <div className="brand">
        <div className="brand__mark" aria-hidden="true">
          CA
        </div>
        <div>
          <h1>cyp-agent</h1>
          <p>半自动加密货币多智能体交易仪表盘</p>
        </div>
      </div>

      <div className="status-strip" aria-label="系统状态">
        <span className="status-pill">
          <Activity size={15} />
          模式 <b>{health?.display_mode ?? health?.mode ?? "--"}</b>
        </span>
        <span className="status-pill">
          <RadioTower size={15} />
          SSE <b>{streamLabel(streamStatus)}</b>
        </span>
        <span className="status-pill">
          分析 <b>{health?.llm ? "规则 + LLM" : "规则模式"}</b>
        </span>
        <span className="status-pill">
          场所 <b>{configuredVenues}/{totalVenues}</b>
        </span>
        <span className={`status-pill ${killOn ? "status-pill--danger" : "status-pill--ok"}`}>
          <span className="status-dot" />
          {killOn ? "停机" : "运行"}
        </span>
      </div>

      <div className="header-actions">
        <button
          className={`command-button command-button--settings ${settingsOpen ? "is-on" : ""}`}
          type="button"
          onClick={onOpenSettings}
          aria-expanded={settingsOpen}
          aria-controls="settings-drawer"
          title="打开设置"
        >
          <Settings size={17} />
          <span>设置</span>
        </button>
        <button
          className="command-button command-button--primary"
          type="button"
          onClick={onRun}
          disabled={running || Boolean(runDisabledReason)}
          title={runDisabledReason ?? "触发一轮闭环"}
        >
          <Play size={17} />
          <span>{running ? "触发中" : runDisabledReason ? "暂不可用" : "触发一轮"}</span>
        </button>
        <button
          className={`command-button command-button--danger ${killOn ? "is-on" : ""}`}
          type="button"
          onClick={onToggleKill}
          disabled={switchingKill}
          title={killOn ? "解除 Kill Switch" : "启用 Kill Switch"}
        >
          <Power size={17} />
          <span>{killOn ? "解除停机" : "Kill Switch"}</span>
        </button>
      </div>
    </header>
  );
}
