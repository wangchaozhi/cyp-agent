import { BrainCircuit, ChevronDown, Coins, Play, Power, RadioTower, Settings, ShieldAlert, ShieldCheck } from "lucide-react";

import type { HealthStatus, RuntimeMode, VenueInfo } from "../../shared/api/types";
import type { StreamStatus } from "../../shared/hooks/useEventStream";

interface SystemHeaderProps {
  health: HealthStatus | null;
  venues: VenueInfo[] | null;
  streamStatus: StreamStatus;
  running: boolean;
  switchingMode: boolean;
  switchingKill: boolean;
  settingsOpen: boolean;
  mode: RuntimeMode;
  analysisSymbol: string;
  analysisSymbols: string[];
  runDisabledReason: string | null;
  onAnalysisSymbolChange: (symbol: string) => void;
  onModeChange: (mode: RuntimeMode) => void;
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
  switchingMode,
  switchingKill,
  settingsOpen,
  mode,
  analysisSymbol,
  analysisSymbols,
  runDisabledReason,
  onAnalysisSymbolChange,
  onModeChange,
  onRun,
  onToggleKill,
  onOpenSettings,
}: SystemHeaderProps) {
  const configuredVenues = venues?.filter((venue) => venue.configured).length ?? 0;
  const totalVenues = venues?.length ?? 0;
  const killOn = Boolean(health?.kill);
  const paperModeLabel = health?.display_mode === "OKX Demo" ? "OKX Demo" : "Paper 模拟";

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
        <span className={`status-pill status-pill--stream status-pill--${streamStatus === "open" ? "ok" : "warn"}`}>
          <RadioTower size={14} />
          <span>{streamLabel(streamStatus)}</span>
        </span>
        <span className="status-pill status-pill--analysis">
          <BrainCircuit size={14} />
          <span>{health?.llm ? "规则 + LLM" : "规则引擎"}</span>
        </span>
        <span
          className="status-pill status-pill--venues"
          title={`已配置 ${configuredVenues} / 共 ${totalVenues} 个场所；不代表已开放实盘下单`}
        >
          已配置场所 <b>{configuredVenues}/{totalVenues}</b>
        </span>
      </div>

      <div className="header-actions">
        <label
          className={`mode-switcher mode-switcher--${mode}`}
          title={mode === "live" ? "Live 只读：允许分析，实盘执行被安全锁禁止" : `${paperModeLabel}：订单仅发送到模拟环境`}
        >
          {mode === "live" ? <ShieldAlert size={15} /> : <ShieldCheck size={15} />}
          <span aria-hidden="true">
            <small>运行模式</small>
            <strong>{mode === "live" ? "Live 只读" : paperModeLabel}</strong>
          </span>
          <ChevronDown size={13} />
          <select
            aria-label="运行模式"
            value={mode}
            onChange={(event) => onModeChange(event.target.value as RuntimeMode)}
            disabled={switchingMode || running || !health}
          >
            <option value="paper">{paperModeLabel}</option>
            <option value="live">Live 只读</option>
          </select>
        </label>
        <label className="analysis-target" title="选择本轮分析币种">
          <Coins size={15} />
          <span aria-hidden="true">
            <small>分析币种</small>
            <strong>{analysisSymbol || "暂无币种"}</strong>
          </span>
          <ChevronDown size={13} />
          <select
            aria-label="分析币种"
            value={analysisSymbol}
            onChange={(event) => onAnalysisSymbolChange(event.target.value)}
            disabled={running || !analysisSymbols.length}
          >
            {!analysisSymbols.length ? <option value="">暂无币种</option> : null}
            {analysisSymbols.map((symbol) => <option key={symbol} value={symbol}>{symbol}</option>)}
          </select>
        </label>
        <button
          className="command-button command-button--primary topbar-run"
          type="button"
          onClick={onRun}
          disabled={running || Boolean(runDisabledReason)}
          title={runDisabledReason ?? `运行 ${analysisSymbol} 一轮分析与决策`}
        >
          <Play size={16} fill="currentColor" />
          <span>{running ? "运行中" : runDisabledReason ? "暂不可用" : "运行分析"}</span>
        </button>
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
