import { CandlestickChart, LogOut, RefreshCw } from "lucide-react";

import type { Position } from "../../shared/api/types";
import { formatAmount, formatCompact, formatPercent, sideClass, sideLabel, toNumber } from "../../shared/lib/format";
import { EmptyState } from "../../shared/ui/EmptyState";
import { Panel } from "../../shared/ui/Panel";

interface PositionsPanelProps {
  positions: Position[];
  loading?: boolean;
  error?: string | null;
  closingKey?: string | null;
  onRequestClose: (position: Position) => void;
  onRetry?: () => void;
}

export function positionKey(position: Position): string {
  return `${position.venue}-${position.symbol}-${position.instrument}`;
}

function pnlTone(position: Position): string {
  const pnl = toNumber(position.unrealized_pnl);
  return pnl > 0 ? "tone-long" : pnl < 0 ? "tone-short" : "tone-muted";
}

export function PositionsPanel({
  positions,
  loading = false,
  error = null,
  closingKey = null,
  onRequestClose,
  onRetry,
}: PositionsPanelProps) {
  const empty = positions.length === 0;

  return (
    <Panel
      title="当前持仓"
      icon={<CandlestickChart size={16} />}
      className={`positions-panel ${empty ? "panel--compact-empty" : ""}`.trim()}
    >
      {positions.length ? (
        <>
          <div className="table-wrap positions-table-view">
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
                  const tone = pnlTone(position);
                  const key = positionKey(position);
                  const closing = closingKey === key;
                  return (
                    <tr key={key}>
                      <td>
                        <strong>{position.symbol}</strong>
                        <small>{position.venue}</small>
                        {position.chain ? <small className="tone-short" title={position.tx_hash ?? undefined}>链上·保护依赖监控存活</small> : null}
                      </td>
                      <td className={sideClass(position.side)}>{sideLabel(position.side)}</td>
                      <td>{formatCompact(position.size_base, 6)}</td>
                      <td>{formatAmount(position.entry_price)}</td>
                      <td>{formatAmount(position.mark_price ?? position.entry_price)}</td>
                      <td className={tone}>{formatAmount(position.unrealized_pnl)}</td>
                      <td className={tone}>{formatPercent(position.unrealized_pnl_pct)}</td>
                      <td>{position.leverage}x{position.margin_mode ? <small>{position.margin_mode === "isolated" ? "逐仓" : "全仓"}</small> : null}</td>
                      <td className={position.liq_price != null ? "tone-short" : "tone-muted"}>{position.liq_price != null ? formatAmount(position.liq_price) : "—"}</td>
                      <td>{position.margin_used != null ? formatAmount(position.margin_used) : "—"}</td>
                      <td>{position.funding_rate != null ? formatPercent(position.funding_rate, 4) : "—"}</td>
                      <td>
                        <button className="icon-command icon-command--danger" type="button" disabled={closing} onClick={() => onRequestClose(position)} title="确认后按当前价平仓">
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

          <div className="positions-card-list">
            {positions.map((position) => {
              const tone = pnlTone(position);
              const key = positionKey(position);
              const closing = closingKey === key;
              return (
                <article className="position-card" key={key}>
                  <div className="position-card__heading">
                    <div><strong>{position.symbol}</strong><span>{position.venue} · {position.instrument}</span></div>
                    <span className={`side-chip ${sideClass(position.side)}`}>{sideLabel(position.side)}</span>
                  </div>
                  <div className="position-card__metrics">
                    <span><small>数量</small><strong>{formatCompact(position.size_base, 6)}</strong></span>
                    <span><small>入场 / 当前</small><strong>{formatAmount(position.entry_price)} / {formatAmount(position.mark_price ?? position.entry_price)}</strong></span>
                    <span><small>浮盈</small><strong className={tone}>{formatAmount(position.unrealized_pnl)} · {formatPercent(position.unrealized_pnl_pct)}</strong></span>
                    <span><small>杠杆 / 爆仓价</small><strong>{position.leverage}x / {position.liq_price != null ? formatAmount(position.liq_price) : "—"}</strong></span>
                  </div>
                  {position.chain ? <p className="position-card__warning">链上仓位依赖监控服务持续运行</p> : null}
                  <button className="command-button icon-command--danger position-card__close" type="button" disabled={closing} onClick={() => onRequestClose(position)}>
                    <LogOut size={15} />{closing ? "正在平仓" : "确认平仓"}
                  </button>
                </article>
              );
            })}
          </div>
        </>
      ) : error ? (
        <EmptyState
          compact
          action={onRetry ? <button className="command-button" type="button" onClick={onRetry}><RefreshCw size={14} />重新同步</button> : null}
        >
          <strong>持仓读取失败</strong>
          <span>当前不能确认账户是否空仓，请勿依据旧数据继续操作。</span>
        </EmptyState>
      ) : loading ? (
        <EmptyState compact><strong>正在同步持仓</strong><span>首次同步完成前不展示空仓结论。</span></EmptyState>
      ) : (
        <EmptyState compact><strong>当前无持仓</strong><span>运行分析或等待自动策略产生有效交易后，这里会显示实时仓位。</span></EmptyState>
      )}
    </Panel>
  );
}
