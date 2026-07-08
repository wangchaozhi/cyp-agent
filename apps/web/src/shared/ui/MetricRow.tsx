import type { ReactNode } from "react";

export function MetricRow({ label, value }: { label: ReactNode; value: ReactNode }) {
  return (
    <div className="metric-row">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}
