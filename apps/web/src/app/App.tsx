import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AlertTriangle, CheckCircle2, RefreshCw, ShieldAlert, X } from "lucide-react";

import { PendingApprovals } from "../features/approvals/PendingApprovals";
import { BacktestPanel } from "../features/backtest/BacktestPanel";
import { EventStream } from "../features/events/EventStream";
import { AppSidebar } from "../features/health/AppSidebar";
import { SystemHeader } from "../features/health/SystemHeader";
import { MarketPanel } from "../features/market/MarketPanel";
import { OverviewStrip } from "../features/overview/OverviewStrip";
import { PortfolioPanel } from "../features/portfolio/PortfolioPanel";
import { positionKey, PositionsPanel } from "../features/positions/PositionsPanel";
import { RiskPanel } from "../features/risk/RiskPanel";
import { SettingsPanel } from "../features/settings/SettingsPanel";
import { TokenUsagePanel } from "../features/token-usage/TokenUsagePanel";
import { cypApi } from "../shared/api/client";
import type { ApprovalRequest, DashboardEvent, Position, RuntimeMode, RuntimeSettingsUpdate } from "../shared/api/types";
import { useEventStream } from "../shared/hooks/useEventStream";
import { usePollingResource } from "../shared/hooks/usePollingResource";
import { formatAmount, formatCompact, sideLabel } from "../shared/lib/format";
import { ConfirmDialog } from "../shared/ui/ConfirmDialog";
import { SectionHeading } from "../shared/ui/SectionHeading";

const MAX_EVENTS = 160;
const ACTIVE_POSITION_POLL_MS = 5_000;
const IDLE_POSITION_POLL_MS = 60_000;

export function positionPollingInterval(positionCount: number): number {
  return positionCount > 0 ? ACTIVE_POSITION_POLL_MS : IDLE_POSITION_POLL_MS;
}

type Notice = { tone: "ok" | "warn" | "bad"; message: string } | null;
type Confirmation =
  | { kind: "clear-kill" }
  | { kind: "close-position"; position: Position }
  | null;

function refreshAll(resources: Array<() => Promise<void>>) {
  void Promise.allSettled(resources.map((refresh) => refresh()));
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "操作失败";
}

function formatUpdatedAt(timestamp: number | null): string {
  if (!timestamp) return "尚未完成同步";
  return new Date(timestamp).toLocaleTimeString("zh-CN", { hour12: false });
}

export default function App() {
  const [positionPollInterval, setPositionPollInterval] = useState(IDLE_POSITION_POLL_MS);
  const health = usePollingResource(cypApi.health, 5000);
  const readiness = usePollingResource(cypApi.ready, 5000);
  const venues = usePollingResource(cypApi.venues, 10000);
  const runtimeSettings = usePollingResource(cypApi.settings, 10000);
  const pending = usePollingResource(cypApi.pending, 3000);
  const positions = usePollingResource(cypApi.positions, positionPollInterval);
  const risk = usePollingResource(cypApi.risk, positionPollInterval);
  const portfolio = usePollingResource(cypApi.portfolio, positionPollInterval);
  const metrics = usePollingResource(cypApi.metrics, 10000);
  const [events, setEvents] = useState<DashboardEvent[]>([]);
  const [marketSymbols, setMarketSymbols] = useState<string[]>([]);
  const [analysisSymbol, setAnalysisSymbol] = useState("");
  const [running, setRunning] = useState(false);
  const [switchingMode, setSwitchingMode] = useState(false);
  const [switchingAutomation, setSwitchingAutomation] = useState(false);
  const [switchingKill, setSwitchingKill] = useState(false);
	const [reconciling, setReconciling] = useState(false);
  const [refreshingCritical, setRefreshingCritical] = useState(false);
  const [confirmation, setConfirmation] = useState<Confirmation>(null);
  const [confirmingAction, setConfirmingAction] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [settingsSection, setSettingsSection] = useState<"general" | "symbols">("general");
  const [notice, setNotice] = useState<Notice>(null);
  const settingsDialogRef = useRef<HTMLElement | null>(null);
  const settingsCloseRef = useRef<HTMLButtonElement | null>(null);
  const queuedRefreshesRef = useRef(new Set<() => Promise<void>>());
  const refreshTimerRef = useRef<number | null>(null);
  const apiError = [
    health.error,
    readiness.error,
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
  const criticalTimestamps = [
    readiness.lastUpdatedAt,
    positions.lastUpdatedAt,
    risk.lastUpdatedAt,
    portfolio.lastUpdatedAt,
  ].filter((value): value is number => value !== null);
  const criticalLastUpdatedAt = criticalTimestamps.length ? Math.min(...criticalTimestamps) : null;
  const criticalStale = readiness.stale || positions.stale || risk.stale || portfolio.stale;
  const criticalInitialLoading = !criticalLastUpdatedAt
    && (readiness.loading || positions.loading || risk.loading || portfolio.loading);

  const refreshCritical = useCallback(async () => {
    if (refreshingCritical) return;
    setRefreshingCritical(true);
    await Promise.allSettled([
      readiness.refresh(),
      positions.refresh(),
      risk.refresh(),
      portfolio.refresh(),
    ]);
    setRefreshingCritical(false);
  }, [portfolio.refresh, positions.refresh, readiness.refresh, refreshingCritical, risk.refresh]);

  const scheduleRefresh = useCallback((resources: Array<() => Promise<void>>) => {
    resources.forEach((refresh) => queuedRefreshesRef.current.add(refresh));
    if (refreshTimerRef.current !== null) return;
    refreshTimerRef.current = window.setTimeout(() => {
      const queued = [...queuedRefreshesRef.current];
      queuedRefreshesRef.current.clear();
      refreshTimerRef.current = null;
      refreshAll(queued);
    }, 100);
  }, []);

  useEffect(() => () => {
    if (refreshTimerRef.current !== null) window.clearTimeout(refreshTimerRef.current);
    queuedRefreshesRef.current.clear();
  }, []);

  useEffect(() => {
    setPositionPollInterval(positionPollingInterval(positions.data?.length ?? 0));
  }, [positions.data?.length]);

  const handleEvent = useCallback(
    (event: DashboardEvent) => {
      setEvents((current) => {
        const duplicate = current.some((item) => (
          item.ts === event.ts && item.type === event.type && item.run_id === event.run_id
        ));
        return duplicate ? current : [event, ...current].slice(0, MAX_EVENTS);
      });

      if (["awaiting_approval", "approval_decided"].includes(event.type)) {
        scheduleRefresh([pending.refresh]);
      }
      if (["run_started", "executed", "reviewed", "run_done", "automated_exit"].includes(event.type)) {
        scheduleRefresh([positions.refresh, risk.refresh, portfolio.refresh, readiness.refresh]);
      }
      if (["risk_assessed", "killswitch"].includes(event.type)) {
        scheduleRefresh([health.refresh, readiness.refresh, risk.refresh, runtimeSettings.refresh]);
      }
			if (event.type === "token_budget_alert") {
				scheduleRefresh([metrics.refresh]);
				setNotice({
					tone: event.level === "paused" ? "bad" : "warn",
					message: event.level === "paused"
						? "今日模型预算已到上限：新模型分析已暂停，持仓监控与自动平仓继续运行"
						: `模型预算使用率已达到 ${((event.ratio ?? 0) * 100).toFixed(0)}%`,
				});
			}
    },
    [health.refresh, metrics.refresh, pending.refresh, portfolio.refresh, positions.refresh, readiness.refresh, risk.refresh, runtimeSettings.refresh, scheduleRefresh],
  );

  const streamStatus = useEventStream(handleEvent);

	const reconcileNow = useCallback(async () => {
		if (reconciling) return;
		setReconciling(true);
		try {
			const report = await cypApi.reconcile();
			setNotice({
				tone: report.ok ? "ok" : "warn",
				message: report.ok ? "账户、持仓、保护单与订单日志已完成安全对账" : "对账仍有差异，新开仓继续冻结",
			});
			refreshAll([readiness.refresh, positions.refresh, risk.refresh, portfolio.refresh]);
		} catch (error) {
			setNotice({ tone: "bad", message: `重新对账失败：${errorMessage(error)}` });
			void readiness.refresh();
		} finally {
			setReconciling(false);
		}
	}, [portfolio.refresh, positions.refresh, readiness.refresh, reconciling, risk.refresh]);
  // The backend has a deterministic rules path and intentionally supports
  // running without an LLM. Only block while the backend health state is not
  // available; an unconfigured model is an informational status, not a gate.
  const runDisabledReason = !health.data
    ? "等待后端状态"
    : !readiness.data
      ? "等待交易安全状态"
      : !readiness.data.execution_ready
        ? readiness.data.safety.reason
          || (health.data.kill ? "Kill Switch 已启用" : readiness.data.reasons.join("；"))
          || "交易执行尚未就绪"
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
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const focusFrame = window.requestAnimationFrame(() => settingsCloseRef.current?.focus());

    function closeOnEscape(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setSettingsOpen(false);
        return;
      }
      if (event.key !== "Tab" || !settingsDialogRef.current) return;
      const focusable = Array.from(settingsDialogRef.current.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), [href], [tabindex]:not([tabindex="-1"])',
      ));
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }

    window.addEventListener("keydown", closeOnEscape);
    return () => {
      window.cancelAnimationFrame(focusFrame);
      window.removeEventListener("keydown", closeOnEscape);
      document.body.style.overflow = previousOverflow;
      previousFocus?.focus();
    };
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
      await Promise.all([runtimeSettings.refresh(), health.refresh(), readiness.refresh(), risk.refresh()]);
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

  const setKillState = async (next: boolean): Promise<boolean> => {
    setSwitchingKill(true);
    setNotice(null);
    try {
      await cypApi.setKillSwitch(next);
      await Promise.all([health.refresh(), readiness.refresh(), risk.refresh(), runtimeSettings.refresh()]);
      setNotice({ tone: "ok", message: next ? "Kill Switch 已启用" : "Kill Switch 已解除" });
      return true;
    } catch (error) {
      setNotice({ tone: "bad", message: `切换失败：${errorMessage(error)}` });
      return false;
    } finally {
      setSwitchingKill(false);
    }
  };

  const requestToggleKill = () => {
    if (health.data?.kill) {
      setConfirmation({ kind: "clear-kill" });
      return;
    }
    void setKillState(true);
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
          ? "策略自动化已开启：扫描、Kelly 开仓、审批、主动退出与受控反向将按各自开关运行"
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
      await Promise.all([runtimeSettings.refresh(), health.refresh(), readiness.refresh()]);
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
      await Promise.all([positions.refresh(), readiness.refresh(), risk.refresh(), portfolio.refresh()]);
      setNotice({ tone: "ok", message: `已平仓 ${position.symbol}，均价=${result.avg_price ?? "-"}` });
    } catch (error) {
      setNotice({ tone: "bad", message: `平仓失败：${errorMessage(error)}` });
      throw error;
    }
  };

  const confirmDangerousAction = async () => {
    if (!confirmation || confirmingAction) return;
    setConfirmingAction(true);
    try {
      if (confirmation.kind === "clear-kill") {
        if (await setKillState(false)) setConfirmation(null);
      } else {
        await closePosition(confirmation.position);
        setConfirmation(null);
      }
    } catch {
      // The action already publishes a detailed notice. Keep the dialog open
      // so the operator can review the failure or cancel explicitly.
    } finally {
      setConfirmingAction(false);
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
            onToggleKill={requestToggleKill}
            onOpenSettings={() => {
              setSettingsSection("general");
              setSettingsOpen(true);
            }}
          />

          <div className="toast-stack" aria-live="polite">
            {apiError ? <div className="app-alert" role="alert"><span>后端连接异常：{apiError}</span></div> : null}
            {readiness.data && !readiness.data.execution_ready ? (
              <div className="app-safety-alert" role="alert">
                <span>新开仓已冻结：{readiness.data.safety.reason || (health.data?.kill ? "Kill Switch 已启用" : readiness.data.reasons.join("；") || "交易执行尚未就绪")}。持仓监控与自动平仓继续运行。</span>
						{readiness.data.safety.frozen ? (
							<button type="button" className="safety-reconcile" disabled={reconciling || readiness.data.reconciling} onClick={() => void reconcileNow()}>
								{reconciling || readiness.data.reconciling ? "对账中…" : "重新对账"}
							</button>
						) : null}
              </div>
            ) : null}
            {notice ? (
              <div className={`app-notice app-notice--${notice.tone}`} role="status">
                <span>{notice.message}</span>
                <button type="button" onClick={() => setNotice(null)} aria-label="关闭通知"><X size={14} /></button>
              </div>
            ) : null}
          </div>

          <main id="main-content" className="dashboard-stack">
            {runDisabledReason ? (
              <div className="execution-gate" role="status">
                <ShieldAlert size={17} />
                <span><strong>运行分析暂不可用</strong>{runDisabledReason}</span>
              </div>
            ) : null}

            <div className={`data-trust-bar ${criticalStale ? "is-stale" : criticalInitialLoading ? "is-loading" : "is-fresh"}`}>
              <div>
                {criticalStale ? <AlertTriangle size={16} /> : <CheckCircle2 size={16} />}
                <span>
                  <strong>{criticalStale ? "关键账户数据可能已过期" : criticalInitialLoading ? "正在进行首次账户同步" : "关键账户数据已同步"}</strong>
                  最后成功更新：{formatUpdatedAt(criticalLastUpdatedAt)}
                </span>
              </div>
              <button className="data-trust-bar__refresh" type="button" disabled={refreshingCritical} onClick={() => void refreshCritical()}>
                <RefreshCw size={14} className={refreshingCritical ? "is-spinning" : ""} />
                {refreshingCritical ? "同步中" : "立即同步"}
              </button>
            </div>

            <OverviewStrip
              health={health.data}
              pending={pending.data ?? []}
              positions={positions.data ?? []}
              risk={risk.data}
              portfolio={portfolio.data}
              metrics={metrics.data}
              streamStatus={streamStatus}
            />

            <section id="operations" className="workspace-section" aria-label="交易与执行">
              <SectionHeading
                index="01 / TRADING DESK"
                title="交易与执行"
                description="先确认实时持仓和待审批提案，再沿时间线追踪每一轮执行。"
              />
              <div className="dashboard-operations">
                <PositionsPanel
                  positions={positions.data ?? []}
                  loading={positions.loading}
                  error={positions.error}
                  closingKey={confirmation?.kind === "close-position" && confirmingAction ? positionKey(confirmation.position) : null}
                  onRequestClose={(position) => setConfirmation({ kind: "close-position", position })}
                  onRetry={() => void positions.refresh()}
                />
              </div>
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
                index="02 / PORTFOLIO CONTROL"
                title="资产与风险"
                description="检查风险预算、回撤、保证金健康度与组合集中度。"
              />
              <div className="dashboard-metrics">
                <RiskPanel risk={risk.data} />
                <PortfolioPanel portfolio={portfolio.data} />
              </div>
            </section>

            <section id="market" className="workspace-section" aria-label="市场情报">
              <SectionHeading
                index="03 / MARKET INTELLIGENCE"
                title="市场情报"
                description="比较多个资产的相对强弱、实时价格与跨场所差异。"
              />
              <MarketPanel
                watchlist={runtimeSettings.data?.watchlist ?? null}
                onSelectionChange={setMarketSymbols}
              />
            </section>

			<section id="token-usage" className="workspace-section" aria-label="模型成本控制">
				<SectionHeading
					index="04 / AI COST CONTROL"
					title="模型成本控制"
					description="先查看今日预算摘要，需要时再展开供应商、模型与调用明细。"
				/>
				<TokenUsagePanel />
			</section>

            <section id="backtest" className="workspace-section" aria-label="策略实验室">
              <SectionHeading
                index="05 / STRATEGY LAB"
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
            ref={settingsDialogRef}
            id="settings-drawer"
            className="settings-drawer"
            role="dialog"
            aria-modal="true"
            aria-label="设置"
            onClick={(event) => event.stopPropagation()}
          >
            <button
              ref={settingsCloseRef}
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

      <ConfirmDialog
        open={confirmation !== null}
        title={confirmation?.kind === "close-position"
          ? `确认平仓 ${confirmation.position.symbol}？`
          : "确认解除安全停机？"}
        description={confirmation?.kind === "close-position"
          ? "将按当前市场价格关闭该仓位。订单提交后可能立即成交，无法撤销。"
          : "解除后系统会恢复允许的分析与交易流程。请先确认触发停机的问题已经处理。"}
        details={confirmation?.kind === "close-position" ? (
          <div className="confirm-position-details">
            <span><b>场所</b>{confirmation.position.venue}</span>
            <span><b>方向</b>{sideLabel(confirmation.position.side)}</span>
            <span><b>数量</b>{formatCompact(confirmation.position.size_base, 8)}</span>
            <span><b>当前价</b>{formatAmount(confirmation.position.mark_price ?? confirmation.position.entry_price)}</span>
          </div>
        ) : (
          <div className="confirm-position-details">
            <span><b>当前模式</b>{runtimeSettings.data?.mode ?? health.data?.mode ?? "--"}</span>
            <span><b>当前持仓</b>{positions.data?.length ?? 0} 笔</span>
          </div>
        )}
        confirmLabel={confirmation?.kind === "close-position" ? "确认平仓" : "解除停机"}
        busyLabel={confirmation?.kind === "close-position" ? "正在平仓…" : "正在解除…"}
        busy={confirmingAction}
        onCancel={() => {
          if (!confirmingAction) setConfirmation(null);
        }}
        onConfirm={() => void confirmDangerousAction()}
      />
    </div>
  );
}
