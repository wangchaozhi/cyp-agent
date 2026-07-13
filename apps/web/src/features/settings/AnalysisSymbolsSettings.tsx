import { useEffect, useRef, useState } from "react";
import { Coins, Plus, Trash2 } from "lucide-react";

import type { RuntimeSettings, RuntimeSettingsUpdate } from "../../shared/api/types";

interface AnalysisSymbolsSettingsProps {
  settings: RuntimeSettings;
  focus: boolean;
  onSave: (payload: RuntimeSettingsUpdate) => Promise<void>;
}

const MAX_WATCHLIST_SIZE = 12;
const POPULAR_BASES = ["BTC", "ETH", "SOL", "BNB", "XRP", "DOGE", "ADA", "AVAX", "LINK", "SUI"];
const SYMBOL_PATTERN = /^[A-Z0-9]{2,20}\/[A-Z0-9]{2,12}(?::[A-Z0-9]{2,12})?$/;

export function normalizeWatchlistSymbol(raw: string, okxDemo: boolean): string | null {
  let symbol = raw.trim().toUpperCase();
  if (/\s/.test(symbol)) return null;
  if (/^[A-Z0-9]{2,20}$/.test(symbol)) {
    symbol = `${symbol}/USDT${okxDemo ? ":USDT" : ""}`;
  } else if (okxDemo && /^[A-Z0-9]{2,20}\/USDT$/.test(symbol)) {
    symbol += ":USDT";
  }
  if (!SYMBOL_PATTERN.test(symbol)) return null;
  if (okxDemo && !symbol.endsWith("/USDT:USDT")) return null;
  return symbol;
}

export function AnalysisSymbolsSettings({ settings, focus, onSave }: AnalysisSymbolsSettingsProps) {
  const [watchlist, setWatchlist] = useState(settings.watchlist);
  const [input, setInput] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const sectionRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const okxDemo = settings.execution_venue === "okx" && settings.okx.demo;
  const popularSymbols = POPULAR_BASES.map((base) => `${base}/USDT${okxDemo ? ":USDT" : ""}`);
  const changed = watchlist.join(",") !== settings.watchlist.join(",");

  useEffect(() => {
    setWatchlist(settings.watchlist);
  }, [settings.watchlist]);

  useEffect(() => {
    if (!focus) return;
    const frame = window.requestAnimationFrame(() => {
      sectionRef.current?.scrollIntoView({ block: "start" });
      inputRef.current?.focus({ preventScroll: true });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [focus]);

  const add = (raw: string) => {
    setError(null);
    const symbol = normalizeWatchlistSymbol(raw, okxDemo);
    if (!symbol) {
      setError(okxDemo ? "OKX Demo 请使用 BTC/USDT:USDT 这类 USDT 永续格式" : "币种格式示例：BTC/USDT");
      return;
    }
    if (watchlist.includes(symbol)) {
      setInput("");
      return;
    }
    if (watchlist.length >= MAX_WATCHLIST_SIZE) {
      setError(`最多配置 ${MAX_WATCHLIST_SIZE} 个分析币种`);
      return;
    }
    setWatchlist((current) => [...current, symbol]);
    setInput("");
  };

  const save = async () => {
    if (!watchlist.length) {
      setError("至少保留一个分析币种");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await onSave({ watchlist });
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存币种失败");
    } finally {
      setSaving(false);
    }
  };

  return (
    <div ref={sectionRef} className="settings-section settings-symbol-manager">
      <div className="settings-section__title settings-section__title--split">
        <span><Coins size={14} />分析币种</span>
        <b>{watchlist.length}/{MAX_WATCHLIST_SIZE}</b>
      </div>
      <p className="muted-line">
        顶部“分析币种”只展示这里保存的币种。{okxDemo ? "当前为 OKX Demo，仅配置 USDT 永续。" : "可输入币种简称或完整交易对。"}
      </p>
      <div className="symbol-manager__tokens" aria-label="已配置分析币种">
        {watchlist.length ? watchlist.map((symbol) => (
          <span key={symbol}>
            {symbol}
            <button
              type="button"
              onClick={() => {
                setError(null);
                setWatchlist((current) => current.filter((item) => item !== symbol));
              }}
              aria-label={`移除 ${symbol}`}
              title={`移除 ${symbol}`}
            >
              <Trash2 size={11} />
            </button>
          </span>
        )) : <em>尚未添加币种</em>}
      </div>
      <div className="symbol-manager__input">
        <input
          ref={inputRef}
          value={input}
          placeholder={okxDemo ? "输入 SOL 或 SOL/USDT:USDT" : "输入 SOL 或 SOL/USDT"}
          onChange={(event) => {
            setInput(event.target.value);
            setError(null);
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              add(input);
            }
          }}
        />
        <button className="icon-command" type="button" onClick={() => add(input)} disabled={!input.trim()} title="添加币种">
          <Plus size={15} />
          <span>添加</span>
        </button>
      </div>
      <div className="symbol-manager__suggestions" aria-label="常用币种">
        <small>快捷添加</small>
        {popularSymbols.map((symbol) => (
          <button
            key={symbol}
            type="button"
            disabled={watchlist.includes(symbol) || watchlist.length >= MAX_WATCHLIST_SIZE}
            onClick={() => add(symbol)}
          >
            + {symbol.split("/")[0]}
          </button>
        ))}
      </div>
      {error ? <div className="inline-alert symbol-manager__error">{error}</div> : null}
      <div className="symbol-manager__footer">
        <span>{changed ? "有未保存更改" : "币种池已同步"}</span>
        <button
          className="command-button command-button--primary"
          type="button"
          onClick={() => void save()}
          disabled={saving || !changed || !watchlist.length}
        >
          {saving ? "保存中" : "保存币种池"}
        </button>
      </div>
    </div>
  );
}
