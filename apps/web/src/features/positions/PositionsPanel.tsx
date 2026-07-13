import { CandlestickChart, LogOut } from "lucide-react";
import { useState } from "react";

import type { Position } from "../../shared/api/types";
import { formatAmount, formatCompact, formatPercent, sideClass, sideLabel, toNumber } from "../../shared/lib/format";
import { EmptyState } from "../../shared/ui/EmptyState";
import { Panel } from "../../shared/ui/Panel";

interface PositionsPanelProps {
  positions: Position[];
  onClose: (position: Position) => Promise<void>;
}

export function PositionsPanel({ positions, onClose }: PositionsPanelProps) {
  const [closingKey, setClosingKey] = useState<string | null>(null);

  const close = async (position: Position) => {
    const key = `${position.venue}-${position.symbol}-${position.instrument}`;
    setClosingKey(key);
    try {
      await onClose(position);
    } finally {
      setClosingKey(null);
    }
  };

  return (
    <Panel title="当前持仓" icon={<CandlestickChart size={16} />} className="positions-panel">
      {positions.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>标的</th>
                <th>方向</th>
                <th>数量</th>
                <th>入场</th>
                <th>当前价</th>
                <th>浮盈</th>
                <th>浮盈率</th>
                <th>杠杆</th>
                <th>爆仓价</th>
                <th>保证金</th>
                <th>资金费</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {positions.map((position) => {
                const pnl = toNumber(position.unrealized_pnl);
                const pnlClass = pnl > 0 ? "tone-long" : pnl < 0 ? "tone-short" : "tone-muted";
                const key = `${position.venue}-${position.symbol}-${position.instrument}`;
                const closing = closingKey === key;

                return (
                  <tr key={key}>
                    <td>
                      <strong>{position.symbol}</strong>
                      <small>{position.venue}</small>
                      {position.chain ? (
                        <small className="tone-short" title={position.tx_hash ?? undefined}>
                          链上·保护依赖监控存活
                        </small>
                      ) : null}
                    </td>
                    <td className={sideClass(position.side)}>{sideLabel(position.side)}</td>
                    <td>{formatCompact(position.size_base, 6)}</td>
                    <td>{formatAmount(position.entry_price)}</td>
                    <td>{formatAmount(position.mark_price ?? position.entry_price)}</td>
                    <td className={pnlClass}>{formatAmount(position.unrealized_pnl)}</td>
                    <td className={pnlClass}>{formatPercent(position.unrealized_pnl_pct)}</td>
                    <td>
                      {position.leverage}x
                      {position.margin_mode ? <small>{position.margin_mode === "isolated" ? "逐仓" : "全仓"}</small> : null}
                    </td>
                    <td className={position.liq_price != null ? "tone-short" : "tone-muted"}>
                      {position.liq_price != null ? formatAmount(position.liq_price) : "—"}
                    </td>
                    <td>{position.margin_used != null ? formatAmount(position.margin_used) : "—"}</td>
                    <td>{position.funding_rate != null ? formatPercent(position.funding_rate, 4) : "—"}</td>
                    <td>
                      <button
                        className="icon-command icon-command--danger"
                        type="button"
                        disabled={closing}
                        onClick={() => void close(position)}
                        title="按当前价平仓"
                      >
                        <LogOut size={14} />
                        <span>{closing ? "平仓中" : "平仓"}</span>
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <EmptyState>无持仓</EmptyState>
      )}
    </Panel>
  );
}
