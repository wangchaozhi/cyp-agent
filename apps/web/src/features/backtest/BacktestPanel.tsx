import { LineChart, Play, RotateCcw } from "lucide-react";
import { type FormEvent, useMemo, useState } from "react";

import { cypApi } from "../../shared/api/client";
import type { BacktestReport, BacktestRequest } from "../../shared/api/types";
import {
  formatAmount,
  formatCompact,
  formatPercent,
  sideClass,
  sideLabel,
  toNumber,
} from "../../shared/lib/format";
import { EmptyState } from "../../shared/ui/EmptyState";
import { MetricRow } from "../../shared/ui/MetricRow";
import { Panel } from "../../shared/ui/Panel";

const DEFAULT_REQUEST: BacktestRequest = {
  symbol: "BTC/USDT",
  bars: 300,
  window: 60,
  seed: 7,
  drift: 0.001,
  vol: 0.01,
  data: "synthetic",
  timeframe: "1h",
  fee_rate: 0.0004,
  slippage_bps: 5,
  spread_bps: 2,
  funding_rate: 0,
};

const TIMEFRAMES = ["1m", "5m", "15m", "30m", "1h", "4h", "1d"];

type NumberField = Exclude<keyof BacktestRequest, "symbol" | "data" | "timeframe">;

const NUMBER_FIELDS: Array<{
  name: NumberField;
  label: string;
  min: number;
  max: number;
  step: number;
}> = [
  { name: "bars", label: "Bars", min: 80, max: 5000, step: 10 },
  { name: "window", label: "窗口", min: 20, max: 1000, step: 5 },
  { name: "seed", label: "Seed", min: 0, max: 1000000, step: 1 },
  { name: "drift", label: "Drift", min: -0.05, max: 0.05, step: 0.0005 },
  { name: "vol", label: "Vol", min: 0.0001, max: 0.2, step: 0.001 },
  { name: "fee_rate", label: "手续费率", min: 0, max: 0.01, step: 0.0001 },
  { name: "slippage_bps", label: "滑点 bps", min: 0, max: 1000, step: 1 },
  { name: "spread_bps", label: "点差 bps", min: 0, max: 1000, step: 1 },
  { name: "funding_rate", label: "每周期资金费", min: -0.01, max: 0.01, step: 0.0001 },
];

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "回测失败";
}

function buildEquityPolyline(curve: number[]) {
  const width = 720;
  const height = 180;
  const pad = 10;
  if (!curve.length) {
    return { width, height, points: "", min: 0, max: 0 };
  }

  const min = Math.min(...curve);
  const max = Math.max(...curve);
  const span = max - min || 1;
  const points = curve
    .map((value, index) => {
      const x = pad + (index / Math.max(1, curve.length - 1)) * (width - pad * 2);
      const y = height - pad - ((value - min) / span) * (height - pad * 2);
      return `${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");
  return { width, height, points, min, max };
}

export function BacktestPanel() {
  const [request, setRequest] = useState<BacktestRequest>(DEFAULT_REQUEST);
  const [report, setReport] = useState<BacktestReport | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const chart = useMemo(
    () => buildEquityPolyline(report?.equity_curve ?? []),
    [report?.equity_curve],
  );

  const updateNumber = (name: NumberField, value: number) => {
    setRequest((current) => ({ ...current, [name]: Number.isFinite(value) ? value : 0 }));
  };

  const runBacktest = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setLoading(true);
    setError(null);
    try {
      const symbol = request.symbol?.trim() || DEFAULT_REQUEST.symbol;
      const result = await cypApi.backtest({ ...request, symbol });
      setReport(result);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  };

  const reset = () => {
    setRequest(DEFAULT_REQUEST);
    setError(null);
  };

  return (
    <Panel
      title="策略回测"
      icon={<LineChart size={16} />}
      actions={
        <button className="icon-command" type="button" onClick={reset} disabled={loading} title="重置参数">
          <RotateCcw size={15} />
        </button>
      }
    >
      <form className="backtest-form" onSubmit={runBacktest}>
        <label>
          <span>标的</span>
          <input
            value={request.symbol ?? ""}
            onChange={(event) => setRequest((current) => ({ ...current, symbol: event.target.value }))}
            placeholder="BTC/USDT"
          />
        </label>

        <label>
          <span>数据源</span>
          <select
            value={request.data ?? "synthetic"}
            onChange={(event) =>
              setRequest((current) => ({ ...current, data: event.target.value as "synthetic" | "cex" }))
            }
          >
            <option value="synthetic">合成历史</option>
            <option value="cex">真实历史（CEX）</option>
          </select>
        </label>

        {request.data === "cex" ? (
          <label>
            <span>周期</span>
            <select
              value={request.timeframe ?? "1h"}
              onChange={(event) => setRequest((current) => ({ ...current, timeframe: event.target.value }))}
            >
              {TIMEFRAMES.map((tf) => (
                <option key={tf} value={tf}>{tf}</option>
              ))}
            </select>
          </label>
        ) : null}

        <button className="command-button command-button--primary backtest-run" type="submit" disabled={loading}>
          <Play size={15} />
          {loading ? "运行中" : "运行回测"}
        </button>

        <details className="backtest-advanced">
          <summary>
            <strong>高级参数</strong>
            <span>数据长度、信号窗口、模拟假设与交易成本</span>
          </summary>
          <div className="backtest-advanced__fields">
            {NUMBER_FIELDS.map((field) => (
              <label key={field.name}>
                <span>{field.label}</span>
                <input
                  type="number"
                  min={field.min}
                  max={field.max}
                  step={field.step}
                  value={request[field.name]}
                  onChange={(event) => updateNumber(field.name, event.currentTarget.valueAsNumber)}
                />
              </label>
            ))}
          </div>
        </details>
      </form>

      {error ? <div className="inline-alert">{error}</div> : null}

      {report ? (
        <div className="backtest-report">
          <div className="metric-stack backtest-metrics">
            <MetricRow label="期初 / 期末净值" value={`${formatAmount(report.metrics.initial_equity)} / ${formatAmount(report.metrics.final_equity)}`} />
            <MetricRow label="总收益" value={formatPercent(report.metrics.total_return, 2)} />
            <MetricRow label="最大回撤" value={formatPercent(report.metrics.max_drawdown, 2)} />
            <MetricRow label="夏普" value={formatCompact(report.metrics.sharpe, 4)} />
            <MetricRow label="交易数 / 胜率" value={`${report.metrics.n_trades} / ${formatPercent(report.metrics.win_rate, 1)}`} />
            <MetricRow label="总交易成本" value={formatAmount(report.metrics.total_costs)} />
            <MetricRow
              label="盈亏比"
              value={report.metrics.profit_factor === null ? "∞" : formatCompact(report.metrics.profit_factor, 4)}
            />
          </div>

          <div className="backtest-chart">
            <div className="backtest-chart__meta">
              <strong>{report.symbol}</strong>
              <span>{report.n_bars} bars · window {report.params.window}</span>
              <span>{formatAmount(chart.min)} - {formatAmount(chart.max)}</span>
            </div>
            <svg viewBox={`0 0 ${chart.width} ${chart.height}`} role="img" aria-label="equity curve">
              <line x1="10" y1={chart.height - 10} x2={chart.width - 10} y2={chart.height - 10} />
              <polyline points={chart.points} />
            </svg>
          </div>

          {report.trades.length ? (
            <div className="table-wrap backtest-trades">
              <table>
                <thead>
                  <tr>
                    <th>区间</th>
                    <th>方向</th>
                    <th>入场</th>
                    <th>出场</th>
                    <th>盈亏</th>
                    <th>成本</th>
                  </tr>
                </thead>
                <tbody>
                  {report.trades.slice(0, 10).map((trade) => (
                    <tr key={`${trade.bar_in}-${trade.bar_out}-${trade.side}`}>
                      <td>{trade.bar_in} - {trade.bar_out}</td>
                      <td className={sideClass(trade.side)}>{sideLabel(trade.side)}</td>
                      <td>{formatAmount(trade.entry)}</td>
                      <td>{formatAmount(trade.exit)}</td>
                      <td className={toNumber(trade.pnl) >= 0 ? "tone-long" : "tone-short"}>
                        {formatAmount(trade.pnl)}
                      </td>
                      <td>{formatAmount(trade.costs)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <EmptyState>无已平仓交易</EmptyState>
          )}

          {report.lessons?.length ? (
            <ul className="arb-hints backtest-lessons">
              {report.lessons.slice(-5).map((lesson, index) => (
                <li key={`${index}-${lesson}`}>{lesson}</li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : (
        <EmptyState>暂无回测报告</EmptyState>
      )}
    </Panel>
  );
}
