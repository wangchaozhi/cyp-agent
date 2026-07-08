import { CandlestickChart } from "lucide-react";

import type { Position } from "../../shared/api/types";
import { formatAmount, formatCompact, sideClass, sideLabel } from "../../shared/lib/format";
import { EmptyState } from "../../shared/ui/EmptyState";
import { Panel } from "../../shared/ui/Panel";

export function PositionsPanel({ positions }: { positions: Position[] }) {
  return (
    <Panel title="持仓" icon={<CandlestickChart size={16} />}>
      {positions.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>标的</th>
                <th>方向</th>
                <th>数量</th>
                <th>入场</th>
                <th>杠杆</th>
              </tr>
            </thead>
            <tbody>
              {positions.map((position) => (
                <tr key={`${position.venue}-${position.symbol}-${position.side}`}>
                  <td>
                    <strong>{position.symbol}</strong>
                    <small>{position.venue}</small>
                  </td>
                  <td className={sideClass(position.side)}>{sideLabel(position.side)}</td>
                  <td>{formatCompact(position.size_base, 6)}</td>
                  <td>{formatAmount(position.entry_price)}</td>
                  <td>{position.leverage}x</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <EmptyState>无持仓</EmptyState>
      )}
    </Panel>
  );
}
