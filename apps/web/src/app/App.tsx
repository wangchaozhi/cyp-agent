import { useCallback, useEffect, useMemo, useState } from "react";
import { X } from "lucide-react";

import { PendingApprovals } from "../features/approvals/PendingApprovals";
import { BacktestPanel } from "../features/backtest/BacktestPanel";
import { EventStream } from "../features/events/EventStream";
import { AppSidebar } from "../features/health/AppSidebar";
import { SystemHeader } from "../features/health/SystemHeader";
import { MarketPanel } from "../features/market/MarketPanel";
import { OverviewStrip } from "../features/overview/OverviewStrip";
import { PortfolioPanel } from "../features/portfolio/PortfolioPanel";
import { PositionsPanel } from "../features/positions/PositionsPanel";
import { RiskPanel } from "../features/risk/RiskPanel";
import { SettingsPanel } from "../features/settings/SettingsPanel";
import { cypApi } from "../shared/api/client";
import type { ApprovalRequest, DashboardEvent, Position, RuntimeMode, RuntimeSettingsUpdate } from "../shared/api/types";
import { useEventStream } from "../shared/hooks/useEventStream";
import { usePollingResource } from "../shared/hooks/usePollingResource";
import { SectionHeading } from "../shared/ui/SectionHeading";

const MAX_EVENTS = 160;

type Notice = { tone: "ok" | "warn" | "bad"; message: string } | null;

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
  const metrics = usePollingResource(cypApi.metrics, 10000);
  const [events, setEvents] = useState<DashboardEvent[]>([]);
  const [marketSymbols, setMarketSymbols] = useState<string[]>([]);
  const [analysisSymbol, setAnalysisSymbol] = useState("");
  const [running, setRunning] = useState(false);
  const [switchingMode, setSwitchingMode] = useState(false);
  const [switchingAutomation, setSwitchingAutomation] = useState(false);
  const [switchingKill, setSwitchingKill] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [settingsSection, setSettingsSection] = useState<"general" | "symbols">("general");
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
  const analysisSymbols = useMemo(() => {
    const configured = runtimeSettings.data?.watchlist ?? [];
    const source = configured.length ? configured : marketSymbols;
    return [...new Set(source.map((symbol) => symbol.trim()).filter(Boolean))];
  }, [marketSymbols, runtimeSettings.data?.watchlist]);

  const handleEvent = useCallback(
    (event: DashboardEvent) => {
      setEvents((current) => [event, ...current].slice(0, MAX_EVENTS));

      if (["awaiting_approval", "approval_decided"].includes(event.type)) {
        refreshAll([pending.refresh]);
      }
      if (["run_started", "executed", "reviewed", "run_done", "automated_exit"].includes(event.type)) {
        refreshAll([positions.refresh, risk.refresh, portfolio.refresh]);
      }
      if (["risk_assessed", "killswitch"].includes(event.type)) {
        refreshAll([health.refresh, risk.refresh, runtimeSettings.refresh]);
      }
    },
    [health.refresh, pending.refresh, portfolio.refresh, positions.refresh, risk.refresh, runtimeSettings.refresh],
  );

  const streamStatus = useEventStream(handleEvent);
  // The backend has a deterministic rules path and intentionally supports
  // running without an LLM. Only block while the backend health state is not
  // available; an unconfigured model is an informational status, not a gate.
  const runDisabledReason = !health.data
    ? "等待后端状态"
    : switchingMode
      ? "正在切换运行模式"
      : !runtimeSettings.data && !marketSymbols.length
        ? "等待币种列表"
        : !analysisSymbol
          ? "未选择分析币种"
          : null;

  useEffect(() => {
    setAnalysisSymbol((current) => (
      analysisSymbols.includes(current) ? current : (analysisSymbols[0] ?? "")
    ));
  }, [analysisSymbols]);

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
    const symbol = analysisSymbol.trim();
    if (runDisabledReason || !symbol) {
      setNotice({ tone: "bad", message: `无法触发：${runDisabledReason ?? "未选择分析币种"}` });
      return;
    }

    setRunning(true);
    setNotice(null);
    try {
      const result = await cypApi.runOnce(symbol);
      await pending.refresh();
      setNotice({ tone: "ok", message: `已触发 ${result.symbol}，run=${result.run_id}` });
    } catch (error) {
      setNotice({ tone: "bad", message: `触发失败：${errorMessage(error)}` });
    } finally {
      setRunning(false);
    }
  };

  const switchMode = async (mode: RuntimeMode) => {
    if (switchingMode || mode === runtimeSettings.data?.mode) return;
    setSwitchingMode(true);
    setNotice(null);
    try {
      const disablesAutomation = mode === "live" && Boolean(runtimeSettings.data?.automation.enabled);
      await cypApi.updateSettings({ mode, ...(disablesAutomation ? { automation: { enabled: false } } : {}) });
      await Promise.all([runtimeSettings.refresh(), health.refresh(), risk.refresh()]);
      setNotice({
        tone: mode === "live" ? "warn" : "ok",
        message: mode === "live"
          ? `已切换到 Live 只读模式：实盘执行被安全锁禁止${disablesAutomation ? "，策略自动化已关闭" : ""}`
          : "已切换到 Paper 模拟模式：所有成交仅作用于模拟账户",
      });
    } catch (error) {
      setNotice({ tone: "bad", message: `模式切换失败：${errorMessage(error)}` });
    } finally {
      setSwitchingMode(false);
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

  const toggleAutomation = async () => {
    const current = runtimeSettings.data?.automation;
    if (!current || switchingAutomation) return;
    const next = !current.enabled;
    setSwitchingAutomation(true);
    setNotice(null);
    try {
      await cypApi.updateSettings({ automation: { enabled: next } });
      await runtimeSettings.refresh();
      setNotice({
        tone: next ? "warn" : "ok",
        message: next
          ? "策略自动化已开启：扫描、审批与主动退出将按各自开关运行"
          : "策略自动化已关闭；交易所原生止损止盈保持有效",
      });
    } catch (error) {
      setNotice({ tone: "bad", message: `自动化切换失败：${errorMessage(error)}` });
    } finally {
      setSwitchingAutomation(false);
    }
  };

  const saveSettings = async (payload: RuntimeSettingsUpdate) => {
    setNotice(null);
    try {
      await cypApi.updateSettings(payload);
      await Promise.all([runtimeSettings.refresh(), health.refresh()]);
      setNotice({
        tone: "ok",
        message: payload.watchlist ? "分析币种已更新" : payload.automation ? "自动化策略已保存" : "设置已保存",
      });
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
      <a className="skip-link" href="#main-content">跳到主要内容</a>

      <div className="app-shell">
        <AppSidebar
          health={health.data}
          streamStatus={streamStatus}
          pendingCount={pending.data?.length ?? 0}
        />

        <div className="app-workspace">
          <SystemHeader
            health={health.data}
            venues={venues.data}
            streamStatus={streamStatus}
            running={running}
            switchingMode={switchingMode}
            switchingAutomation={switchingAutomation}
            switchingKill={switchingKill}
            settingsOpen={settingsOpen}
            mode={runtimeSettings.data?.mode ?? health.data?.mode ?? "paper"}
            automationEnabled={runtimeSettings.data?.automation.enabled ?? false}
            analysisSymbol={analysisSymbol}
            analysisSymbols={analysisSymbols}
            runDisabledReason={runDisabledReason}
            onAnalysisSymbolChange={setAnalysisSymbol}
            onManageAnalysisSymbols={() => {
              setSettingsSection("symbols");
              setSettingsOpen(true);
            }}
            onModeChange={(mode) => void switchMode(mode)}
            onToggleAutomation={() => void toggleAutomation()}
            onRun={() => void runOnce()}
            onToggleKill={() => void toggleKill()}
            onOpenSettings={() => {
              setSettingsSection("general");
              setSettingsOpen(true);
            }}
          />

          <div className="toast-stack" aria-live="polite">
            {apiError ? <div className="app-alert" role="alert"><span>后端连接异常：{apiError}</span></div> : null}
            {notice ? (
              <div className={`app-notice app-notice--${notice.tone}`} role="status">
                <span>{notice.message}</span>
                <button type="button" onClick={() => setNotice(null)} aria-label="关闭通知"><X size={14} /></button>
              </div>
            ) : null}
          </div>

          <main id="main-content" className="dashboard-stack">
            <OverviewStrip
              health={health.data}
              pending={pending.data ?? []}
              positions={positions.data ?? []}
              risk={risk.data}
              portfolio={portfolio.data}
              metrics={metrics.data}
              streamStatus={streamStatus}
            />

            <section id="market" className="workspace-section" aria-label="市场情报">
              <SectionHeading
                index="01 / MARKET INTELLIGENCE"
                title="市场情报"
                description="比较多个资产的相对强弱、实时价格与跨场所差异。"
              />
              <MarketPanel
                watchlist={runtimeSettings.data?.watchlist ?? null}
                onSelectionChange={setMarketSymbols}
              />
            </section>

            <section id="operations" className="workspace-section" aria-label="决策与执行">
              <SectionHeading
                index="02 / DECISION FLOW"
                title="决策与执行"
                description="先处理需要人工判断的提案，再沿时间线追踪每一轮运行。"
              />
              <div className="dashboard-hero">
                <PendingApprovals
                  items={pending.data ?? []}
                  loading={pending.loading}
                  onDecide={decideApproval}
                />
                <EventStream events={events} status={streamStatus} />
              </div>
            </section>

            <section id="portfolio" className="workspace-section" aria-label="资产与风险">
              <SectionHeading
                index="03 / PORTFOLIO CONTROL"
                title="资产与风险"
                description="从当前持仓进入，检查风险预算、回撤与组合集中度。"
              />
              <div className="dashboard-metrics">
                <PositionsPanel positions={positions.data ?? []} onClose={closePosition} />
                <RiskPanel risk={risk.data} />
                <PortfolioPanel portfolio={portfolio.data} />
              </div>
            </section>

            <section id="backtest" className="workspace-section" aria-label="策略实验室">
              <SectionHeading
                index="04 / STRATEGY LAB"
                title="策略实验室"
                description="用合成或真实历史数据验证参数，在进入决策流程前理解策略表现。"
              />
              <BacktestPanel />
            </section>
          </main>
        </div>
      </div>

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
              focusSection={settingsSection}
              onSave={saveSettings}
            />
          </aside>
        </div>
      ) : null}
    </div>
  );
}
