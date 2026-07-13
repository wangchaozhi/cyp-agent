import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { ArrowLeftRight, ChevronDown, Plus, TrendingUp, X } from "lucide-react";

import type { MarketHistoryResponse, MarketHistorySeries, MarketSnapshotInfo } from "../../shared/api/types";
import { cypApi } from "../../shared/api/client";
import { formatCompact, formatPercent, toNumber } from "../../shared/lib/format";
import { EmptyState } from "../../shared/ui/EmptyState";
import { Panel } from "../../shared/ui/Panel";
import { MarketChart } from "./MarketChart";

const MAX_SELECTED = 5;
const POPULAR_ASSETS = ["BTC", "ETH", "SOL", "BNB", "XRP", "DOGE", "ADA", "AVAX", "LINK", "SUI"];
const RANGES = {
  "24h": { label: "24小时", timeframe: "1h", limit: 24 },
  "7d": { label: "7天", timeframe: "4h", limit: 42 },
  "30d": { label: "30天", timeframe: "1d", limit: 30 },
} as const;

type RangeKey = keyof typeof RANGES;

function unique(values: string[]): string[] {
  return [...new Set(values.map((value) => value.trim().toUpperCase()).filter(Boolean))];
}

function symbolFromBase(base: string, sample: string): string {
  const [pair, settlement] = sample.split(":", 2);
  const quote = pair.split("/")[1] || "USDT";
  return `${base}/${quote}${settlement ? `:${settlement}` : ""}`;
}

export function marketSymbolOptions(watchlist: string[]): string[] {
  const normalizedWatchlist = unique(watchlist);
  const sample = normalizedWatchlist[0] || "BTC/USDT";
  return unique([
    ...normalizedWatchlist,
    ...POPULAR_ASSETS.map((asset) => symbolFromBase(asset, sample)),
  ]);
}

function normalizedCustomSymbol(value: string, sample: string): string | null {
  const normalized = value.trim().toUpperCase().replace(/-/g, "/");
  if (/^[A-Z0-9]{2,12}$/.test(normalized)) return symbolFromBase(normalized, sample);
  if (/^[A-Z0-9]{2,12}\/[A-Z0-9]{2,12}(?::[A-Z0-9]{2,12})?$/.test(normalized)) return normalized;
  return null;
}

function representativePrice(market: MarketSnapshotInfo | undefined): number | null {
  if (!market) return null;
  const best = toNumber(market.best_buy.price, Number.NaN);
  if (Number.isFinite(best) && best > 0) return best;
  const ticker = Object.values(market.tickers).map(Number).find((value) => Number.isFinite(value) && value > 0);
  return ticker ?? null;
}

function seriesChange(series: MarketHistorySeries | undefined): number | null {
  if (!series || series.points.length < 2) return null;
  const first = toNumber(series.points[0].close);
  const last = toNumber(series.points[series.points.length - 1].close);
  return first > 0 && last > 0 ? last / first - 1 : null;
}

function assetLabel(symbol: string): string {
  return symbol.split("/")[0];
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "行情请求失败";
}

function startVisiblePolling(load: () => Promise<void>, intervalMs: number): () => void {
  let active = true;
  let running = false;
  let timer = 0;
  const schedule = () => {
    if (!active) return;
    timer = window.setTimeout(() => void run(), intervalMs);
  };
  const run = async () => {
    if (!active || running) return;
    if (document.visibilityState === "hidden" || !navigator.onLine) {
      return;
    }
    running = true;
    try {
      await load();
    } finally {
      running = false;
      schedule();
    }
  };
  const resume = () => {
    if (document.visibilityState === "hidden" || !navigator.onLine || running) return;
    window.clearTimeout(timer);
    void run();
  };
  document.addEventListener("visibilitychange", resume);
  window.addEventListener("online", resume);
  void run();
  return () => {
    active = false;
    window.clearTimeout(timer);
    document.removeEventListener("visibilitychange", resume);
    window.removeEventListener("online", resume);
  };
}

interface MarketPanelProps {
  watchlist: string[] | null;
  onSelectionChange?: (symbols: string[]) => void;
}

export function MarketPanel({ watchlist, onSelectionChange }: MarketPanelProps) {
  const initialized = useRef(false);
  const options = useMemo(() => marketSymbolOptions(watchlist ?? []), [watchlist]);
  const [customOptions, setCustomOptions] = useState<string[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [rangeKey, setRangeKey] = useState<RangeKey>("7d");
  const [markets, setMarkets] = useState<Record<string, MarketSnapshotInfo>>({});
  const [history, setHistory] = useState<MarketHistoryResponse | null>(null);
  const [quotesLoading, setQuotesLoading] = useState(true);
  const [historyLoading, setHistoryLoading] = useState(true);
  const [quotesError, setQuotesError] = useState<string | null>(null);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [selectionMessage, setSelectionMessage] = useState<string | null>(null);
  const [customValue, setCustomValue] = useState("");
  const allOptions = useMemo(() => unique([...options, ...customOptions]), [customOptions, options]);

  useEffect(() => {
    if (watchlist === null || initialized.current || !options.length) return;
    initialized.current = true;
    setSelected(options.slice(0, 2));
  }, [options, watchlist]);

  useEffect(() => {
    onSelectionChange?.(selected);
  }, [onSelectionChange, selected]);

  useEffect(() => {
    if (!selected.length) return undefined;
    let active = true;
    const loadQuotes = async () => {
      setQuotesLoading(true);
      const results = await Promise.allSettled(selected.map((symbol) => cypApi.market(symbol)));
      if (!active) return;
      const next: Record<string, MarketSnapshotInfo> = {};
      let firstError: string | null = null;
      results.forEach((result, index) => {
        if (result.status === "fulfilled") next[selected[index]] = result.value;
        else if (!firstError) firstError = errorMessage(result.reason);
      });
      setMarkets(next);
      setQuotesError(Object.keys(next).length ? null : firstError);
      setQuotesLoading(false);
    };
    const stop = startVisiblePolling(loadQuotes, 15_000);
    return () => {
      active = false;
      stop();
    };
  }, [selected]);

  useEffect(() => {
    if (!selected.length) return undefined;
    let active = true;
    const range = RANGES[rangeKey];
    const loadHistory = async () => {
      setHistoryLoading(true);
      try {
        const next = await cypApi.marketHistory(selected, range.timeframe, range.limit);
        if (!active) return;
        setHistory(next);
        setHistoryError(null);
      } catch (error) {
        if (!active) return;
        setHistoryError(errorMessage(error));
      } finally {
        if (active) setHistoryLoading(false);
      }
    };
    const stop = startVisiblePolling(loadHistory, 60_000);
    return () => {
      active = false;
      stop();
    };
  }, [rangeKey, selected]);

  const toggleSymbol = (symbol: string) => {
    setSelectionMessage(null);
    if (selected.includes(symbol)) {
      if (selected.length === 1) {
        setSelectionMessage("至少保留一个币种");
        return;
      }
      setSelected(selected.filter((item) => item !== symbol));
      return;
    }
    if (selected.length >= MAX_SELECTED) {
      setSelectionMessage(`最多同时对比 ${MAX_SELECTED} 个币种`);
      return;
    }
    setSelected([...selected, symbol]);
  };

  const addCustomSymbol = (event: FormEvent) => {
    event.preventDefault();
    const symbol = normalizedCustomSymbol(customValue, allOptions[0] || "BTC/USDT");
    if (!symbol) {
      setSelectionMessage("请输入如 ETH、ETH/USDT 或 ETH/USDT:USDT 的币种");
      return;
    }
    if (!allOptions.includes(symbol)) setCustomOptions((current) => [...current, symbol]);
    if (!selected.includes(symbol)) {
      if (selected.length >= MAX_SELECTED) {
        setSelectionMessage(`已添加到列表；最多同时对比 ${MAX_SELECTED} 个币种`);
      } else {
        setSelected((current) => [...current, symbol]);
        setSelectionMessage(null);
      }
    }
    setCustomValue("");
  };

  if (watchlist === null) {
    return (
      <Panel title="资产趋势" icon={<ArrowLeftRight size={16} />} className="market-panel">
        <EmptyState>正在加载可选币种</EmptyState>
      </Panel>
    );
  }

  const visibleSeries = (history?.series ?? []).filter((item) => selected.includes(item.symbol));
  const historyBySymbol = Object.fromEntries(visibleSeries.map((item) => [item.symbol, item]));

  return (
    <Panel
      title="资产趋势"
      icon={<ArrowLeftRight size={16} />}
      className="market-panel"
      actions={
        <div className="market-range" aria-label="曲线时间范围">
          {(Object.keys(RANGES) as RangeKey[]).map((key) => (
            <button key={key} type="button" aria-pressed={rangeKey === key} className={rangeKey === key ? "is-active" : ""} onClick={() => setRangeKey(key)}>
              {RANGES[key].label}
            </button>
          ))}
        </div>
      }
    >
      <div className="market-toolbar">
        <div>
          <span className="market-toolbar__eyebrow">已选 {selected.length}/{MAX_SELECTED}</span>
          <div className="market-selected">
            {selected.map((symbol) => (
              <button type="button" key={symbol} onClick={() => toggleSymbol(symbol)} title={`移除 ${symbol}`}>
                {assetLabel(symbol)} <X size={12} />
              </button>
            ))}
          </div>
        </div>
        <details className="market-picker">
          <summary><Plus size={14} />选择更多币种<ChevronDown size={13} /></summary>
          <div className="market-picker__menu">
            <div className="market-picker__options">
              {allOptions.map((symbol) => {
                const checked = selected.includes(symbol);
                return (
                  <label key={symbol}>
                    <input
                      type="checkbox"
                      checked={checked}
                      disabled={!checked && selected.length >= MAX_SELECTED}
                      onChange={() => toggleSymbol(symbol)}
                    />
                    <span>{assetLabel(symbol)}</span>
                    <small>{symbol}</small>
                  </label>
                );
              })}
            </div>
            <form onSubmit={addCustomSymbol} className="market-picker__custom">
              <input value={customValue} onChange={(event) => setCustomValue(event.target.value)} placeholder="输入币种，如 TON" aria-label="自定义币种" />
              <button type="submit" className="icon-command" aria-label="添加自定义币种"><Plus size={14} /></button>
            </form>
          </div>
        </details>
      </div>

      {selectionMessage ? <p className="market-message">{selectionMessage}</p> : null}
      {quotesError ? <div className="inline-alert">实时行情：{quotesError}</div> : null}
      {historyError ? <div className="inline-alert">历史曲线：{historyError}</div> : null}

      <div className="market-chart-card">
        <div className="market-chart-card__heading">
          <div><TrendingUp size={15} /><strong>相对涨跌曲线</strong></div>
          <span>{history?.venue ? `${history.venue.toUpperCase()} · ` : ""}以各币种区间起点归一化为 0%</span>
        </div>
        <MarketChart series={visibleSeries} loading={historyLoading} />
      </div>

      <div className="market-summary-grid" aria-busy={quotesLoading}>
        {selected.map((symbol) => {
          const market = markets[symbol];
          const price = representativePrice(market);
          const change = seriesChange(historyBySymbol[symbol]);
          const venueIds = market ? Object.keys(market.tickers) : [];
          return (
            <article className="market-summary-card" key={symbol}>
              <div className="market-summary-card__top">
                <div>
                  <strong>{assetLabel(symbol)}</strong>
                  <span>{symbol}</span>
                </div>
                {change !== null ? (
                  <em className={change >= 0 ? "tone-long" : "tone-short"}>{change >= 0 ? "+" : ""}{formatPercent(change, 2)}</em>
                ) : <em className="tone-muted">--</em>}
              </div>
              {price !== null ? <b className="market-summary-card__price">{formatCompact(price, price < 1 ? 6 : 2)}</b> : <p className="muted-line">实时行情暂不可用</p>}
              <div className="market-summary-card__meta">
                <span>{venueIds.length ? `${venueIds.length} 家交易所` : "等待报价"}</span>
                <span>{market?.spread_bps != null ? `价差 ${formatCompact(market.spread_bps, 2)} bps` : "价差 --"}</span>
              </div>
              {market && venueIds.length ? (
                <div className="market-summary-card__venues">
                  {venueIds.map((venueId) => (
                    <span key={venueId}>
                      <i>{venueId}</i>
                      <b>{formatCompact(market.tickers[venueId], price !== null && price < 1 ? 6 : 2)}</b>
                      {market.funding_rates[venueId] != null ? <small>费率 {formatPercent(market.funding_rates[venueId], 4)}</small> : null}
                    </span>
                  ))}
                </div>
              ) : null}
              {market?.arb_hints.length ? <p className="market-summary-card__hint">{market.arb_hints[0]}</p> : null}
            </article>
          );
        })}
      </div>
    </Panel>
  );
}
