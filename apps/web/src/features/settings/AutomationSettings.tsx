import { useEffect, useState } from "react";
import { Activity, Calculator, Radar, ShieldCheck } from "lucide-react";

import type { AutomationSettings, RuntimeSettingsUpdate } from "../../shared/api/types";

interface AutomationSettingsProps {
  value: AutomationSettings;
  liveReadOnly?: boolean;
  onSave: (payload: RuntimeSettingsUpdate) => Promise<void>;
}

interface AutomationDraft extends Omit<AutomationSettings, "max_quote"> {
  max_quote: number;
}

function toDraft(value: AutomationSettings): AutomationDraft {
  return { ...value, max_quote: Number(value.max_quote) };
}

export function validateAutomation(value: AutomationDraft): string | null {
  const finite = Object.entries(value).filter(([, item]) => typeof item === "number" && !Number.isFinite(item));
  if (finite.length) return "所有数学参数必须是有效数字";
  if (value.max_risk_score < 0 || value.max_risk_score > 1) return "风险分上限应在 0 到 1 之间";
  if (value.max_quote < 0) return "自动审批金额不能为负数";
  if (value.min_confidence < 0 || value.min_confidence > 1) return "最低置信度应在 0 到 1 之间";
  if (value.min_reward_risk <= 0) return "最低盈亏比必须大于 0";
  if (value.ewma_lambda <= 0 || value.ewma_lambda >= 1) return "EWMA λ 应在 0 到 1 之间";
  if (value.volatility_multiplier < 0 || value.trail_activation_r <= 0 || value.trail_giveback_r <= 0) {
    return "波动率倍数不能为负，跟踪参数必须大于 0";
  }
  if (value.max_holding_minutes <= 0 || value.exit_confirmations < 1 || value.exit_min_samples < 2) {
    return "持仓时间、确认次数和最小样本数不在安全范围内";
  }
  return null;
}

export function AutomationSettingsPanel({ value, liveReadOnly = false, onSave }: AutomationSettingsProps) {
  const [draft, setDraft] = useState<AutomationDraft>(() => toDraft(value));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => setDraft(toDraft(value)), [value]);

  const setToggle = (field: "enabled" | "scan_enabled" | "approval_enabled" | "exit_enabled", checked: boolean) => {
    setError(null);
    setDraft((current) => ({ ...current, [field]: checked }));
  };

  const setNumber = (field: keyof AutomationDraft, raw: string) => {
    setError(null);
    setDraft((current) => ({ ...current, [field]: Number(raw) }));
  };

  const save = async () => {
    const validation = validateAutomation(draft);
    if (validation) {
      setError(validation);
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await onSave({ automation: draft });
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存自动化策略失败");
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="settings-section settings-section--wide automation-settings" id="automation-settings">
      <div className="automation-settings__hero">
        <div>
          <span className={`automation-state ${draft.enabled ? "is-on" : ""}`}>
            <Activity size={13} /> {draft.enabled ? "自动化运行中" : "自动化已关闭"}
          </span>
          <h3>策略自动化</h3>
          <p>自动扫描、数学审批和主动退出可独立控制；总开关关闭后，交易所原生止损止盈仍然有效。</p>
        </div>
        <label className="toggle-control toggle-control--master">
          <input
            type="checkbox"
            checked={draft.enabled}
            disabled={liveReadOnly}
            onChange={(event) => setToggle("enabled", event.target.checked)}
          />
          <span aria-hidden="true" />
          <b>{liveReadOnly ? "Live 锁定" : draft.enabled ? "开启" : "关闭"}</b>
        </label>
      </div>

      <div className="automation-strategy-grid">
        <label className={`automation-strategy ${draft.scan_enabled ? "is-on" : ""}`}>
          <Radar size={16} />
          <span><strong>定时扫描</strong><small>按监控列表自动运行分析</small></span>
          <input type="checkbox" checked={draft.scan_enabled} onChange={(event) => setToggle("scan_enabled", event.target.checked)} />
          <i aria-hidden="true" />
        </label>
        <label className={`automation-strategy ${draft.approval_enabled ? "is-on" : ""}`}>
          <Calculator size={16} />
          <span><strong>数学审批</strong><small>RR、期望值与 Kelly 同时过线</small></span>
          <input type="checkbox" checked={draft.approval_enabled} onChange={(event) => setToggle("approval_enabled", event.target.checked)} />
          <i aria-hidden="true" />
        </label>
        <label className={`automation-strategy ${draft.exit_enabled ? "is-on" : ""}`}>
          <ShieldCheck size={16} />
          <span><strong>主动退出</strong><small>EWMA 波动跟踪与时间止损</small></span>
          <input type="checkbox" checked={draft.exit_enabled} onChange={(event) => setToggle("exit_enabled", event.target.checked)} />
          <i aria-hidden="true" />
        </label>
      </div>

      <div className="automation-parameter-groups">
        <fieldset>
          <legend>自动审批边界</legend>
          <label><span>风险分上限</span><input type="number" min="0" max="1" step="0.01" value={draft.max_risk_score} onChange={(event) => setNumber("max_risk_score", event.target.value)} /></label>
          <label><span>最大名义金额 (USDT)</span><input type="number" min="0" step="10" value={draft.max_quote} onChange={(event) => setNumber("max_quote", event.target.value)} /></label>
          <label><span>最低置信度</span><input type="number" min="0" max="1" step="0.01" value={draft.min_confidence} onChange={(event) => setNumber("min_confidence", event.target.value)} /></label>
          <label><span>最低盈亏比 (RR)</span><input type="number" min="0.1" step="0.1" value={draft.min_reward_risk} onChange={(event) => setNumber("min_reward_risk", event.target.value)} /></label>
        </fieldset>
        <fieldset>
          <legend>主动退出模型</legend>
          <label><span>EWMA λ</span><input type="number" min="0.01" max="0.99" step="0.01" value={draft.ewma_lambda} onChange={(event) => setNumber("ewma_lambda", event.target.value)} /></label>
          <label><span>波动率倍数</span><input type="number" min="0" step="0.1" value={draft.volatility_multiplier} onChange={(event) => setNumber("volatility_multiplier", event.target.value)} /></label>
          <label><span>跟踪启动 (R)</span><input type="number" min="0.1" step="0.1" value={draft.trail_activation_r} onChange={(event) => setNumber("trail_activation_r", event.target.value)} /></label>
          <label><span>最小回吐 (R)</span><input type="number" min="0.1" step="0.1" value={draft.trail_giveback_r} onChange={(event) => setNumber("trail_giveback_r", event.target.value)} /></label>
          <label><span>最长持仓 (分钟)</span><input type="number" min="1" step="15" value={draft.max_holding_minutes} onChange={(event) => setNumber("max_holding_minutes", event.target.value)} /></label>
          <label><span>时间止损阈值 (R)</span><input type="number" step="0.1" value={draft.time_stop_min_r} onChange={(event) => setNumber("time_stop_min_r", event.target.value)} /></label>
          <label><span>退出确认次数</span><input type="number" min="1" step="1" value={draft.exit_confirmations} onChange={(event) => setNumber("exit_confirmations", event.target.value)} /></label>
          <label><span>最小样本数</span><input type="number" min="2" step="1" value={draft.exit_min_samples} onChange={(event) => setNumber("exit_min_samples", event.target.value)} /></label>
        </fieldset>
      </div>

      {error ? <div className="inline-alert automation-settings__error">{error}</div> : null}
      <div className="automation-settings__footer">
        <p>主动退出只发送 reduce-only 市价单，不会反向开仓；Live 模式会锁定总开关，但仍可预先调整参数。</p>
        <button className="command-button command-button--primary" type="button" disabled={saving} onClick={() => void save()}>
          {saving ? "保存中" : "保存自动化策略"}
        </button>
      </div>
    </section>
  );
}
