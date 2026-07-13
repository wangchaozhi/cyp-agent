import { useEffect, useState } from "react";
import { Clock3 } from "lucide-react";

import type { RuntimeSettings, RuntimeSettingsUpdate } from "../../shared/api/types";

const FREQUENCY_OPTIONS = [
  { seconds: 60, label: "每 1 分钟", note: "响应最快 · Token 100%" },
  { seconds: 300, label: "每 5 分钟", note: "均衡 · Token 约 20%" },
  { seconds: 900, label: "每 15 分钟", note: "节省 · Token 约 7%" },
  { seconds: 1800, label: "每 30 分钟", note: "低频 · Token 约 3%" },
] as const;

export function estimateDailySymbolScans(seconds: number, symbolCount: number): number {
  if (!Number.isFinite(seconds) || seconds <= 0 || symbolCount <= 0) return 0;
  return Math.floor(86_400 / seconds) * symbolCount;
}

interface ScanFrequencySettingsProps {
  settings: RuntimeSettings;
  onSave: (payload: RuntimeSettingsUpdate) => Promise<void>;
}

export function ScanFrequencySettings({ settings, onSave }: ScanFrequencySettingsProps) {
  const [seconds, setSeconds] = useState(settings.intervals.scan);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => setSeconds(settings.intervals.scan), [settings.intervals.scan]);

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      await onSave({ scan_interval: seconds });
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存分析频率失败");
    } finally {
      setSaving(false);
    }
  };

  const dailyScans = estimateDailySymbolScans(seconds, settings.watchlist.length);
  return (
    <div className="settings-section settings-section--wide">
      <div className="settings-section__title">
        <Clock3 size={14} />
        <span>分析频率与 Token</span>
      </div>
      <div className="settings-form scan-frequency-form">
        {error ? <div className="inline-alert">{error}</div> : null}
        <label>
          <span>自动分析频率</span>
          <select value={seconds} onChange={(event) => setSeconds(Number(event.target.value))}>
            {FREQUENCY_OPTIONS.map((option) => (
              <option key={option.seconds} value={option.seconds}>
                {option.label} · {option.note}
              </option>
            ))}
          </select>
        </label>
        <button
          className="command-button command-button--primary"
          type="button"
          disabled={saving || seconds === settings.intervals.scan}
          onClick={() => void save()}
        >
          {saving ? "保存中" : "应用分析频率"}
        </button>
        <p className="muted-line scan-frequency-form__summary">
          当前 {settings.watchlist.length} 个币种，约 {dailyScans.toLocaleString("zh-CN")} 个币种分析轮次/天。修改后立即生效并持久化；5 秒持仓退出检测不调用 LLM。
        </p>
      </div>
    </div>
  );
}
