import { Blocks } from "lucide-react";

import type { PortfolioSnapshot, SymbolExposure } from "../../shared/api/types";
import { formatAmount, toNumber } from "../../shared/lib/format";
import { EmptyState } from "../../shared/ui/EmptyState";
import { Meter } from "../../shared/ui/Meter";
import { MetricRow } from "../../shared/ui/MetricRow";
import { Panel } from "../../shared/ui/Panel";

function ClusterExposure({
  label,
  value,
  limit,
}: {
  label: string;
  value: unknown;
  limit: unknown;
}) {
  return (
    <div className="cluster-row">
      <MetricRow label={label} value={`${formatAmount(value)} / ${formatAmount(limit, 0)}`} />
      <Meter value={value} max={limit} />
    </div>
  );
}

function ExposureHeatmap({ items, gross }: { items: SymbolExposure[]; gross: number }) {
  if (!items.length || gross <= 0) return null;
  return (
    <div className="exposure-heatmap">
      {items.map((item) => {
        const long = toNumber(item.long);
        const short = toNumber(item.short);
        const net = long - short;
        const weight = (long + short) / gross;
        const tone = net > 0 ? "long" : net < 0 ? "short" : "muted";
        return (
          <div
            key={item.symbol}
            className={`exposure-cell exposure-cell--${tone}`}
            style={{ "--heat": Math.min(1, Math.max(0.15, weight)) } as React.CSSProperties}
            title={`${item.symbol} 多 ${formatAmount(long)} / 空 ${formatAmount(short)}（${item.cluster} 簇）`}
          >
            <span className="exposure-cell__symbol">{item.symbol}</span>
            <span className="exposure-cell__value">{formatAmount(long + short, 0)}</span>
          </div>
        );
      })}
    </div>
  );
}

export function PortfolioPanel({ portfolio }: { portfolio: PortfolioSnapshot | null }) {
  if (!portfolio) {
    return (
      <Panel title="组合敞口" icon={<Blocks size={16} />}>
        <EmptyState>加载中</EmptyState>
      </Panel>
    );
  }

  return (
    <Panel title="组合敞口" icon={<Blocks size={16} />}>
      <div className="metric-stack">
        <MetricRow label="净值 / 持仓数" value={`${formatAmount(portfolio.equity)} / ${portfolio.n_positions}`} />
        <MetricRow label="总名义敞口" value={formatAmount(portfolio.gross)} />
        <MetricRow label="相关性簇上限" value={formatAmount(portfolio.correlated_limit)} />
        <ClusterExposure label="majors 多" value={portfolio.clusters.major.long} limit={portfolio.correlated_limit} />
        <ClusterExposure label="majors 空" value={portfolio.clusters.major.short} limit={portfolio.correlated_limit} />
        <ClusterExposure label="alt 多" value={portfolio.clusters.alt.long} limit={portfolio.correlated_limit} />
        <ClusterExposure label="alt 空" value={portfolio.clusters.alt.short} limit={portfolio.correlated_limit} />
        <ExposureHeatmap items={portfolio.by_symbol ?? []} gross={toNumber(portfolio.gross)} />
        {!portfolio.n_positions ? <EmptyState>无持仓</EmptyState> : null}
      </div>
    </Panel>
  );
}
