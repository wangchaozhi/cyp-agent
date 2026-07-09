import { ArrowLeftRight } from "lucide-react";

import type { MarketSnapshotInfo } from "../../shared/api/types";
import { formatAmount, formatPercent } from "../../shared/lib/format";
import { EmptyState } from "../../shared/ui/EmptyState";
import { MetricRow } from "../../shared/ui/MetricRow";
import { Panel } from "../../shared/ui/Panel";

export function MarketPanel({ market }: { market: MarketSnapshotInfo | null }) {
  if (!market) {
    return (
      <Panel title="跨所行情" icon={<ArrowLeftRight size={16} />}>
        <EmptyState>加载中</EmptyState>
      </Panel>
    );
  }

  const venueIds = Object.keys(market.tickers);

  return (
    <Panel title={`跨所行情 · ${market.symbol}`} icon={<ArrowLeftRight size={16} />}>
      <div className="metric-stack">
        {venueIds.length ? (
          venueIds.map((venueId) => (
            <MetricRow
              key={venueId}
              label={venueId}
              value={
                <span>
                  {formatAmount(market.tickers[venueId])}
                  {market.funding_rates[venueId] != null ? (
                    <small className="tone-muted"> 费率 {formatPercent(market.funding_rates[venueId], 4)}</small>
                  ) : null}
                </span>
              }
            />
          ))
        ) : (
          <EmptyState>无可用行情（CEX 未配置或网络不可达）</EmptyState>
        )}
        {market.best_buy.venue ? (
          <MetricRow
            label="最优买入 / 卖出"
            value={`${market.best_buy.venue} ${formatAmount(market.best_buy.price)} / ${market.best_sell.venue} ${formatAmount(market.best_sell.price)}`}
          />
        ) : null}
        {market.spread_bps != null ? (
          <MetricRow label="跨所价差" value={`${formatAmount(market.spread_bps)} bps`} />
        ) : null}
        {market.arb_hints.length ? (
          <ul className="arb-hints">
            {market.arb_hints.map((hint) => (
              <li key={hint}>{hint}</li>
            ))}
          </ul>
        ) : null}
      </div>
    </Panel>
  );
}
