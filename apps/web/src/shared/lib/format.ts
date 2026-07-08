import type { Side, Verdict } from "../api/types";

export function toNumber(value: unknown, fallback = 0): number {
  const next = Number(value);
  return Number.isFinite(next) ? next : fallback;
}

export function formatAmount(value: unknown, digits = 2): string {
  return toNumber(value).toLocaleString("zh-CN", {
    maximumFractionDigits: digits,
    minimumFractionDigits: digits,
  });
}

export function formatCompact(value: unknown, digits = 4): string {
  return toNumber(value).toLocaleString("zh-CN", {
    maximumFractionDigits: digits,
  });
}

export function formatPercent(ratio: unknown, digits = 1): string {
  return `${(toNumber(ratio) * 100).toFixed(digits)}%`;
}

export function formatConfidence(value: unknown): string {
  return toNumber(value).toFixed(2);
}

export function formatClock(value: string | undefined): string {
  if (!value) return "--:--:--";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.valueOf())) return value.slice(11, 19);
  return parsed.toLocaleTimeString("zh-CN", { hour12: false });
}

export function clampPercent(value: unknown, max: unknown): number {
  const denominator = toNumber(max);
  if (denominator <= 0) return 0;
  return Math.min(100, Math.max(0, (toNumber(value) / denominator) * 100));
}

export function sideLabel(side: Side): string {
  return side === "long" ? "多" : side === "short" ? "空" : "观望";
}

export function sideClass(side: Side): string {
  return side === "long" ? "tone-long" : side === "short" ? "tone-short" : "tone-muted";
}

export function verdictLabel(verdict: Verdict): string {
  const labels: Record<Verdict, string> = {
    approved: "通过",
    downsized: "降档",
    rejected: "拒绝",
  };
  return labels[verdict];
}
