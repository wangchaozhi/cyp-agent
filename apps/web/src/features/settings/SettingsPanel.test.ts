import { describe, expect, it } from "vitest";

import { normalizeWatchlistSymbol } from "./AnalysisSymbolsSettings";
import { validateAutomation } from "./AutomationSettings";
import { estimateDailySymbolScans } from "./ScanFrequencySettings";

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
    entry_enabled: true,
    approval_enabled: true,
    exit_enabled: true,
    reverse_enabled: false,
    add_enabled: true,
    max_risk_score: 0.5,
    max_quote: 200,
    min_entry_quote: 20,
    min_confidence: 0.65,
    min_reward_risk: 1.5,
    kelly_scale: 0.25,
    add_min_confidence: 0.75,
    add_min_profit_r: 0.5,
    add_risk_decay: 0.5,
    add_max_position_fraction: 0.5,
    add_cooldown_minutes: 60,
    max_adds_per_position: 2,
    reverse_min_confidence: 0.75,
    reverse_min_reward_risk: 2,
    reverse_confirmations: 2,
    reverse_signal_minutes: 30,
    reverse_cooldown_minutes: 60,
    max_reversals_per_day: 2,
    ewma_lambda: 0.94,
    volatility_multiplier: 3,
    trail_activation_r: 1,
    trail_giveback_r: 0.5,
    profit_target_r: 1.5,
    loss_cut_r: 0.5,
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
    expect(validateAutomation({ ...valid, reverse_confirmations: 0 })).toContain("反向");
  });
});

describe("scan frequency configuration", () => {
  it("estimates token-driving symbol scans for the selected preset", () => {
    expect(estimateDailySymbolScans(60, 7)).toBe(10_080);
    expect(estimateDailySymbolScans(300, 7)).toBe(2_016);
    expect(estimateDailySymbolScans(900, 7)).toBe(672);
    expect(estimateDailySymbolScans(0, 7)).toBe(0);
  });
});
