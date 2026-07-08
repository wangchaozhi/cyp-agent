import { Blocks } from "lucide-react";

import type { PortfolioSnapshot } from "../../shared/api/types";
import { formatAmount } from "../../shared/lib/format";
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
        {!portfolio.n_positions ? <EmptyState>无持仓</EmptyState> : null}
      </div>
    </Panel>
  );
}
