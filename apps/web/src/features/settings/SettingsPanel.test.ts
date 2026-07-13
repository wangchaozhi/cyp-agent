import { describe, expect, it } from "vitest";

import { normalizeWatchlistSymbol } from "./AnalysisSymbolsSettings";

describe("analysis symbol configuration", () => {
  it("expands a base asset for OKX Demo", () => {
    expect(normalizeWatchlistSymbol(" sol ", true)).toBe("SOL/USDT:USDT");
    expect(normalizeWatchlistSymbol("eth/usdt", true)).toBe("ETH/USDT:USDT");
  });

  it("keeps spot symbols for Paper and rejects incompatible Demo pairs", () => {
    expect(normalizeWatchlistSymbol("btc/usdt", false)).toBe("BTC/USDT");
    expect(normalizeWatchlistSymbol("BTC/USDC:USDC", true)).toBeNull();
    expect(normalizeWatchlistSymbol("not a pair", false)).toBeNull();
  });
});
