import { useEffect, useState, type ReactNode } from "react";
import { Download, KeyRound, ServerCog, Settings as SettingsIcon, ShieldCheck } from "lucide-react";

import type { RuntimeSettings, RuntimeSettingsUpdate, VenueInfo } from "../../shared/api/types";
import { setApiToken } from "../../shared/api/client";
import { formatAmount, formatCompact, formatPercent } from "../../shared/lib/format";
import { EmptyState } from "../../shared/ui/EmptyState";
import { MetricRow } from "../../shared/ui/MetricRow";
import { Panel } from "../../shared/ui/Panel";
import { AnalysisSymbolsSettings } from "./AnalysisSymbolsSettings";
import { AutomationSettingsPanel } from "./AutomationSettings";
import { ScanFrequencySettings } from "./ScanFrequencySettings";

interface SettingsPanelProps {
  settings: RuntimeSettings | null;
  venues: VenueInfo[];
  focusSection?: "general" | "symbols";
  onSave: (payload: RuntimeSettingsUpdate) => Promise<void>;
}

type ChipTone = "ok" | "warn" | "bad" | "muted";

function SettingsChip({ tone, children }: { tone: ChipTone; children: ReactNode }) {
  return <span className={`settings-chip settings-chip--${tone}`}>{children}</span>;
}

function formatSeconds(seconds: number): string {
  if (seconds >= 60 && seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

function venueTone(venue: VenueInfo): ChipTone {
  if (!venue.configured) return "bad";
  return venue.read_only ? "warn" : "ok";
}

function venueModeLabel(venue: VenueInfo): string {
  const modes = [
    venue.spot ? "现货" : null,
    venue.perp ? "永续" : null,
    venue.native_protective_orders ? "保护单" : null,
  ].filter(Boolean);
  return modes.length ? modes.join(" / ") : "--";
}

interface SettingsFormState {
  llm_provider: string;
  llm_model: string;
  llm_model_fast: string;
  llm_base_url: string;
  anthropic_api_key: string;
  deepseek_api_key: string;
  api_token: string;
}

function formFromSettings(settings: RuntimeSettings): SettingsFormState {
  return {
    llm_provider: settings.llm_provider,
    llm_model: settings.llm_model,
    llm_model_fast: settings.llm_model_fast,
    llm_base_url: settings.llm_base_url ?? "",
    anthropic_api_key: "",
    deepseek_api_key: "",
    api_token: "",
  };
}

export function SettingsPanel({ settings, venues, focusSection = "general", onSave }: SettingsPanelProps) {
  const [form, setForm] = useState<SettingsFormState | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (settings) {
      setForm(formFromSettings(settings));
    }
  }, [settings]);

  if (!settings) {
    return (
      <Panel title="系统设置" icon={<SettingsIcon size={16} />} className="panel--settings">
        <EmptyState>加载中</EmptyState>
      </Panel>
    );
  }

  const guard = settings.live_guard;
  const risk = settings.risk;
  const currentForm = form ?? formFromSettings(settings);

  const updateField = (field: keyof SettingsFormState, value: string) => {
    setError(null);
    setForm((current) => ({ ...(current ?? formFromSettings(settings)), [field]: value }));
  };

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      setApiToken(currentForm.api_token);
      await onSave({
        llm_provider: currentForm.llm_provider,
        llm_model: currentForm.llm_model,
        llm_model_fast: currentForm.llm_model_fast,
        anthropic_api_key: currentForm.anthropic_api_key,
        deepseek_api_key: currentForm.deepseek_api_key,
      });
      setForm((current) => current ? { ...current, anthropic_api_key: "", deepseek_api_key: "" } : current);
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存设置失败");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Panel title="系统设置" icon={<SettingsIcon size={16} />} className="panel--settings">
      <div className="settings-layout">
        <div className="settings-chip-row" aria-label="运行状态">
          <SettingsChip tone={settings.kill ? "bad" : "ok"}>{settings.kill ? "停机" : "运行"}</SettingsChip>
          <SettingsChip tone={settings.mode === "live" ? "warn" : "muted"}>{settings.mode}</SettingsChip>
          <SettingsChip tone={settings.execution_venue === "paper" ? "muted" : "ok"}>
            执行 {settings.execution_venue}
          </SettingsChip>
          <SettingsChip tone={settings.okx.configured ? "ok" : "bad"}>
            OKX {settings.okx.configured ? "已配置" : "未配置"}
          </SettingsChip>
          <SettingsChip tone={settings.okx.demo ? "ok" : "warn"}>{settings.okx.demo ? "Demo" : "实盘"}</SettingsChip>
          <SettingsChip tone={settings.llm_enabled ? "ok" : "bad"}>
            LLM {settings.llm_provider}
          </SettingsChip>
          <SettingsChip tone={settings.automation.enabled ? "ok" : "muted"}>
            自动化 {settings.automation.enabled ? "运行中" : "已关闭"}
          </SettingsChip>
          <SettingsChip tone={guard.ok ? "ok" : "bad"}>{guard.ok ? "校验通过" : "校验未过"}</SettingsChip>
        </div>

        <AnalysisSymbolsSettings settings={settings} focus={focusSection === "symbols"} onSave={onSave} />

        <ScanFrequencySettings settings={settings} onSave={onSave} />

        <AutomationSettingsPanel value={settings.automation} liveReadOnly={settings.mode === "live"} onSave={onSave} />

        <div className="settings-section">
          <div className="settings-section__title">
            <ServerCog size={14} />
            <span>模型设置</span>
          </div>
          <div className="settings-form">
            {error ? <div className="inline-alert">{error}</div> : null}
            <label>
              <span>供应商</span>
              <select
                value={currentForm.llm_provider}
                onChange={(event) => updateField("llm_provider", event.target.value)}
              >
                <option value="anthropic">Anthropic</option>
                <option value="deepseek">DeepSeek</option>
              </select>
            </label>
            <label>
              <span>主模型</span>
              <input
                value={currentForm.llm_model}
                placeholder={currentForm.llm_provider === "deepseek" ? "deepseek-v4-pro" : "claude-opus-4-8"}
                onChange={(event) => updateField("llm_model", event.target.value)}
              />
            </label>
            <label>
              <span>快速模型</span>
              <input
                value={currentForm.llm_model_fast}
                placeholder={currentForm.llm_provider === "deepseek" ? "deepseek-v4-flash" : "claude-haiku-4-5-20251001"}
                onChange={(event) => updateField("llm_model_fast", event.target.value)}
              />
            </label>
            <label>
              <span>Base URL</span>
              <input
                value={currentForm.llm_base_url}
                placeholder={currentForm.llm_provider === "deepseek" ? "https://api.deepseek.com" : "默认"}
                disabled
              />
            </label>
            <label>
              <span>Anthropic Key</span>
              <input
                type="password"
                value={currentForm.anthropic_api_key}
                placeholder={settings.llm_provider === "anthropic" && settings.llm_enabled ? "已配置，留空不变" : "sk-ant-..."}
                onChange={(event) => updateField("anthropic_api_key", event.target.value)}
              />
            </label>
            <label>
              <span>DeepSeek Key</span>
              <input
                type="password"
                value={currentForm.deepseek_api_key}
                placeholder={settings.llm_provider === "deepseek" && settings.llm_enabled ? "已配置，留空不变" : "sk-..."}
                onChange={(event) => updateField("deepseek_api_key", event.target.value)}
              />
            </label>
            <label>
              <span>API 写操作令牌</span>
              <input
                type="password"
                value={currentForm.api_token}
                placeholder={settings.api_auth_enabled ? "输入 CYP_API_TOKEN" : "本机回环访问可留空"}
                onChange={(event) => updateField("api_token", event.target.value)}
              />
            </label>
            <button className="command-button command-button--primary" type="button" onClick={save} disabled={saving}>
              {saving ? "保存中" : "保存设置"}
            </button>
            <p className="muted-line">Base URL 仅可通过启动配置修改；模型密钥不会回显，API 令牌只保存在当前浏览器。</p>
          </div>
        </div>

        <div className="settings-section">
          <div className="settings-section__title">
            <ServerCog size={14} />
            <span>运行状态</span>
          </div>
          <div className="metric-stack">
            <MetricRow label="审批" value={settings.approval} />
            <MetricRow label="执行场所" value={settings.execution_venue} />
            <MetricRow label="行情源" value={settings.data_source} />
            <MetricRow label="扫描 / 监控" value={`${formatSeconds(settings.intervals.scan)} / ${formatSeconds(settings.intervals.monitor)}`} />
            <MetricRow label="并发 / 日志" value={`${settings.runtime.max_concurrency} / ${settings.runtime.log_level}`} />
            <MetricRow
              label="K 线归档"
              value={settings.runtime.ohlcv_archive_enabled ? `PostgreSQL / ${settings.runtime.ohlcv_retention_days} 天` : "已关闭"}
            />
            <MetricRow label="合约策略" value={settings.allow_perp ? "允许" : "关闭"} />
          </div>
					<a className="command-button settings-audit-download" href="/api/audit/export" download>
						<Download size={14} />
						导出订单与成交审计
					</a>
        </div>

        <div className="settings-section">
          <div className="settings-section__title">
            <KeyRound size={14} />
            <span>凭据</span>
          </div>
          <div className="metric-stack">
            <MetricRow label={`CEX (${settings.cex_id})`} value={settings.cex_trading_configured ? "已配置" : "未配置"} />
            <MetricRow label="OKX" value={`${settings.okx.configured ? "已配置" : "未配置"} / ${settings.okx.demo ? "Demo" : "实盘"}`} />
            <MetricRow label="LLM Key" value={settings.llm_enabled ? "已配置" : "未配置"} />
          </div>
        </div>

        <div className="settings-section settings-section--wide">
          <div className="settings-section__title">
            <ShieldCheck size={14} />
            <span>风控限制</span>
          </div>
          <div className="settings-risk-grid">
            <MetricRow label="单笔 / 单仓" value={`${formatPercent(risk.max_risk_per_trade)} / ${formatPercent(risk.max_position_pct)}`} />
            <MetricRow label="总敞口 / 相关簇" value={`${formatPercent(risk.max_gross_exposure, 0)} / ${formatPercent(risk.max_correlated_exposure, 0)}`} />
            <MetricRow label="CVaR / 滑点" value={`${formatPercent(risk.max_cvar_pct)} / ${formatCompact(risk.max_slippage_bps, 0)}bps`} />
            <MetricRow label="杠杆上限 / 步长" value={`${formatCompact(risk.max_leverage, 1)}x / ${formatCompact(risk.leverage_step, 1)}x`} />
            <MetricRow label="保证金 / 爆仓缓冲" value={`${formatPercent(risk.max_margin_pct)} / ${formatPercent(risk.min_liq_buffer)}`} />
            <MetricRow label="止损 / 波动压力" value={`${formatCompact(risk.liq_stop_multiple, 1)}× / ${formatCompact(risk.liq_vol_multiple, 1)}×`} />
            <MetricRow label="清算预留 / 模式" value={`${formatPercent(risk.liq_reserve_pct)} / ${risk.force_isolated ? "逐仓" : "可全仓"}`} />
            <MetricRow
              label="回撤(日/周/总)"
              value={`${formatPercent(risk.daily_drawdown_limit, 0)} / ${formatPercent(risk.weekly_drawdown_limit, 0)} / ${formatPercent(risk.max_drawdown_limit, 0)}`}
            />
            <MetricRow label="下单 / 连亏" value={`${risk.max_orders_per_hour}/h / ${risk.max_consecutive_losses}`} />
            <MetricRow label="审批超时" value={formatSeconds(risk.approval_timeout_seconds)} />
            <MetricRow
              label="预算"
              value={`${settings.budget.max_iterations}轮 / ${formatCompact(settings.budget.max_tokens, 0)} tokens / $${formatAmount(settings.budget.max_cost_usd, 2)}`}
            />
          </div>
          {guard.reasons.length ? <p className="muted-line settings-reasons">{guard.reasons.join("；")}</p> : null}
        </div>

        <div className="settings-section settings-section--wide">
          <div className="table-wrap settings-venue-table">
            <table>
              <thead>
                <tr>
                  <th>场所</th>
                  <th>能力</th>
                  <th>配置</th>
                  <th>权限</th>
                </tr>
              </thead>
              <tbody>
                {venues.length ? (
                  venues.map((venue) => (
                    <tr key={venue.id}>
                      <td>
                        <strong>{venue.id}</strong>
                        <small>{venue.kind.toUpperCase()}</small>
                      </td>
                      <td>{venueModeLabel(venue)}</td>
                      <td>
                        <span className={`settings-chip settings-chip--${venueTone(venue)}`}>
                          {venue.configured ? "已配置" : "未配置"}
                        </span>
                      </td>
                      <td>{venue.read_only ? "只读" : "可交易"}</td>
                    </tr>
                  ))
                ) : (
                  <tr>
                    <td colSpan={4} className="tone-muted">
                      加载中
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </Panel>
  );
}
