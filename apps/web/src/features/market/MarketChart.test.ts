import { describe, expect, it } from "vitest";

import type { MarketHistorySeries } from "../../shared/api/types";
import { buildMarketChartModel } from "./MarketChart";
import { marketSymbolOptions } from "./MarketPanel";

describe("market chart", () => {
  it("normalizes differently priced symbols to percentage changes", () => {
    const series: MarketHistorySeries[] = [
      { symbol: "BTC/USDT", points: [{ ts: "2026-01-01T00:00:00Z", close: 100 }, { ts: "2026-01-02T00:00:00Z", close: 110 }] },
      { symbol: "ETH/USDT", points: [{ ts: "2026-01-01T00:00:00Z", close: 10 }, { ts: "2026-01-02T00:00:00Z", close: 9 }] },
    ];
    const model = buildMarketChartModel(series);
    expect(model?.lines[0].change).toBeCloseTo(10);
    expect(model?.lines[1].change).toBeCloseTo(-10);
    expect(model?.lines.every((line) => line.path.startsWith("M"))).toBe(true);
  });

  it("ignores invalid and single-point series", () => {
    expect(buildMarketChartModel([
      { symbol: "BTC/USDT", points: [{ ts: "invalid", close: 100 }] },
    ])).toBeNull();
  });
});

describe("market symbol options", () => {
  it("keeps the watchlist instrument shape for popular assets", () => {
    const options = marketSymbolOptions(["BTC/USDT:USDT"]);
    expect(options.slice(0, 3)).toEqual(["BTC/USDT:USDT", "ETH/USDT:USDT", "SOL/USDT:USDT"]);
  });
});
