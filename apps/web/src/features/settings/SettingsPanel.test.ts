import { describe, expect, it } from "vitest";

import { normalizeWatchlistSymbol } from "./AnalysisSymbolsSettings";
import { validateAutomation } from "./AutomationSettings";

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

describe("automation strategy configuration", () => {
  const valid = {
    enabled: false,
    scan_enabled: true,
    approval_enabled: true,
    exit_enabled: true,
    max_risk_score: 0.5,
    max_quote: 200,
    min_confidence: 0.65,
    min_reward_risk: 1.5,
    ewma_lambda: 0.94,
    volatility_multiplier: 3,
    trail_activation_r: 1,
    trail_giveback_r: 0.5,
    max_holding_minutes: 360,
    time_stop_min_r: 0,
    exit_confirmations: 2,
    exit_min_samples: 8,
  };

  it("accepts the conservative defaults", () => {
    expect(validateAutomation(valid)).toBeNull();
  });

  it("rejects unsafe probability and sample parameters", () => {
    expect(validateAutomation({ ...valid, min_confidence: 1.2 })).toContain("置信度");
    expect(validateAutomation({ ...valid, exit_min_samples: 1 })).toContain("样本数");
  });
});
