import type { MarketHistorySeries } from "../../shared/api/types";
import { EmptyState } from "../../shared/ui/EmptyState";

const WIDTH = 920;
const HEIGHT = 286;
const PADDING = { top: 22, right: 20, bottom: 34, left: 54 };
const COLORS = ["#46c6b6", "#7ba7e8", "#deb654", "#f06c64", "#a78bfa", "#57b86f"];

interface ChartLine {
  symbol: string;
  color: string;
  path: string;
  change: number;
  lastX: number;
  lastY: number;
}

export interface MarketChartModel {
  width: number;
  height: number;
  lines: ChartLine[];
  grid: Array<{ value: number; y: number }>;
  zeroY: number;
  start: number;
  end: number;
}

function validPoints(series: MarketHistorySeries) {
  return series.points
    .map((point) => ({ ts: new Date(point.ts).valueOf(), close: Number(point.close) }))
    .filter((point) => Number.isFinite(point.ts) && Number.isFinite(point.close) && point.close > 0)
    .sort((left, right) => left.ts - right.ts);
}

export function buildMarketChartModel(series: MarketHistorySeries[]): MarketChartModel | null {
  const normalized = series
    .map((item, index) => {
      const points = validPoints(item);
      if (points.length < 2) return null;
      const base = points[0].close;
      return {
        symbol: item.symbol,
        color: COLORS[index % COLORS.length],
        points: points.map((point) => ({
          ts: point.ts,
          value: ((point.close / base) - 1) * 100,
        })),
      };
    })
    .filter((item): item is NonNullable<typeof item> => item !== null);

  if (!normalized.length) return null;

  const values = normalized.flatMap((item) => item.points.map((point) => point.value));
  const stamps = normalized.flatMap((item) => item.points.map((point) => point.ts));
  let minimum = Math.min(0, ...values);
  let maximum = Math.max(0, ...values);
  const rawRange = maximum - minimum;
  const padding = Math.max(rawRange * 0.12, 0.25);
  minimum -= padding;
  maximum += padding;
  const valueRange = maximum - minimum;
  const start = Math.min(...stamps);
  const end = Math.max(...stamps);
  const timeRange = Math.max(1, end - start);
  const plotWidth = WIDTH - PADDING.left - PADDING.right;
  const plotHeight = HEIGHT - PADDING.top - PADDING.bottom;
  const x = (stamp: number) => PADDING.left + ((stamp - start) / timeRange) * plotWidth;
  const y = (value: number) => PADDING.top + ((maximum - value) / valueRange) * plotHeight;

  const lines = normalized.map((item) => {
    const coordinates = item.points.map((point) => ({ x: x(point.ts), y: y(point.value) }));
    const last = coordinates[coordinates.length - 1];
    return {
      symbol: item.symbol,
      color: item.color,
      change: item.points[item.points.length - 1].value,
      path: coordinates.map((point, index) => `${index ? "L" : "M"}${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(" "),
      lastX: last.x,
      lastY: last.y,
    };
  });
  const grid = Array.from({ length: 5 }, (_, index) => {
    const value = maximum - (valueRange * index) / 4;
    return { value, y: y(value) };
  });
  return { width: WIDTH, height: HEIGHT, lines, grid, zeroY: y(0), start, end };
}

function formatTime(value: number): string {
  return new Date(value).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

function shortSymbol(symbol: string): string {
  return symbol.split("/")[0];
}

export function MarketChart({ series, loading }: { series: MarketHistorySeries[]; loading: boolean }) {
  const model = buildMarketChartModel(series);

  if (!model) {
    return <EmptyState>{loading ? "正在加载历史曲线" : "所选币种暂无可用历史行情"}</EmptyState>;
  }

  return (
    <div className="market-chart">
      <div className="market-chart__legend" aria-label="曲线图例">
        {model.lines.map((line) => (
          <span key={line.symbol}>
            <i style={{ backgroundColor: line.color }} />
            <b>{shortSymbol(line.symbol)}</b>
            <em className={line.change >= 0 ? "tone-long" : "tone-short"}>
              {line.change >= 0 ? "+" : ""}{line.change.toFixed(2)}%
            </em>
          </span>
        ))}
      </div>
      <svg viewBox={`0 0 ${model.width} ${model.height}`} role="img" aria-label="所选虚拟货币价格涨跌曲线">
        {model.grid.map((item) => (
          <g key={item.value}>
            <line className="market-chart__grid" x1={PADDING.left} y1={item.y} x2={model.width - PADDING.right} y2={item.y} />
            <text className="market-chart__axis" x={PADDING.left - 9} y={item.y + 4} textAnchor="end">
              {item.value > 0 ? "+" : ""}{item.value.toFixed(1)}%
            </text>
          </g>
        ))}
        <line className="market-chart__zero" x1={PADDING.left} y1={model.zeroY} x2={model.width - PADDING.right} y2={model.zeroY} />
        {model.lines.map((line) => (
          <g key={line.symbol}>
            <path className="market-chart__line" d={line.path} stroke={line.color} />
            <circle cx={line.lastX} cy={line.lastY} r="4" fill={line.color}>
              <title>{line.symbol} {line.change >= 0 ? "+" : ""}{line.change.toFixed(2)}%</title>
            </circle>
          </g>
        ))}
        <text className="market-chart__axis" x={PADDING.left} y={model.height - 9}>{formatTime(model.start)}</text>
        <text className="market-chart__axis" x={model.width - PADDING.right} y={model.height - 9} textAnchor="end">{formatTime(model.end)}</text>
      </svg>
      {loading ? <span className="market-chart__refreshing">正在刷新…</span> : null}
    </div>
  );
}
