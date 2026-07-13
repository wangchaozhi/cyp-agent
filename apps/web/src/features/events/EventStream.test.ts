import { describe, expect, it } from "vitest";

import type { DashboardEvent } from "../../shared/api/types";
import { eventTone, summarizeEvent } from "./EventStream";

function event(overrides: Partial<DashboardEvent>): DashboardEvent {
  return {
    type: "run_done",
    run_id: "run-1",
    ts: "2026-07-11T15:41:46Z",
    ...overrides,
  };
}

describe("event stream semantics", () => {
  it("renders degraded reports as unavailable instead of fake confidence", () => {
    const summary = summarizeEvent(
      event({
        type: "reports_ready",
        reports: [
          {
            agent: "sentiment",
            stance: "neutral",
            confidence: 0.2,
            signals: [],
            rationale: "无情绪数据",
            degraded: true,
          },
        ],
      }),
    );

    expect(summary).toBe("覆盖=0/1  sentiment:无数据");
    expect(summary).not.toContain("0.20");
  });

  it("describes a flat proposal as an explained observation", () => {
    const summary = summarizeEvent(
      event({
        type: "proposal_ready",
        proposal: {
          symbol: "BTC/USDT:USDT",
          venue: "paper",
          side: "flat",
          instrument: "spot",
          size_quote: "0",
          leverage: 1,
          margin_mode: "isolated",
          entry: { type: "market" },
          stop_loss: null,
          take_profit: [],
          confidence: 0.0877,
          thesis: "多维信号不足或冲突，本轮不开仓。",
          supporting_reports: ["technical", "derivatives"],
        },
      }),
    );

    expect(summary).toContain("观望 BTC/USDT:USDT");
    expect(summary).toContain("强度=0.09");
    expect(summary).toContain("多维信号不足");
  });

  it("labels proposal notional, reference price, and stop price unambiguously", () => {
    const summary = summarizeEvent(
      event({
        type: "proposal_ready",
        proposal: {
          symbol: "ETH/USDT:USDT",
          venue: "okx",
          side: "short",
          instrument: "perp",
          size_quote: "985.39",
          leverage: 2,
          margin_mode: "isolated",
          entry: { type: "market", price: "1785.74" },
          stop_loss: "1817.44",
          take_profit: ["1722.34"],
          confidence: 0.45,
          thesis: "test",
          supporting_reports: ["technical"],
        },
      }),
    );

    expect(summary).toContain("名义金额=985.39 USDT");
    expect(summary).toContain("参考价=1785.74");
    expect(summary).toContain("止损价=1817.44");
    expect(summary).not.toContain("规模=");
  });

  it("does not paint no-trade and rejected runs as successful executions", () => {
    expect(eventTone(event({ status: "no_trade" }))).toBe("event-row--warn");
    expect(eventTone(event({ status: "rejected" }))).toBe("event-row--warn");
    expect(eventTone(event({ status: "executed" }))).toBe("event-row--ok");
    expect(eventTone(event({ status: "execution_failed" }))).toBe("event-row--bad");
  });

	it("explains token budget pauses without implying position monitoring stopped", () => {
		const alert = event({ type: "token_budget_alert", level: "paused", ratio: 1 });
		expect(eventTone(alert)).toBe("event-row--bad");
		expect(summarizeEvent(alert)).toContain("持仓监控与自动平仓继续运行");
	});
});
