import { clampPercent } from "../lib/format";

interface MeterProps {
  value: unknown;
  max: unknown;
}

export function Meter({ value, max }: MeterProps) {
  const percent = clampPercent(value, max);
  const tone = percent >= 100 ? "bad" : percent >= 60 ? "warn" : "ok";

  return (
    <div className={`meter meter--${tone}`} aria-hidden="true">
      <span style={{ width: `${percent}%` }} />
    </div>
  );
}
