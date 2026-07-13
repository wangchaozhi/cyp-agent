import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { cypApi, setApiToken } from "./client";

function fakeStorage(): Storage {
  const values = new Map<string, string>();
  return {
    get length() {
      return values.size;
    },
    clear: () => values.clear(),
    getItem: (key: string) => values.get(key) ?? null,
    key: (index: number) => [...values.keys()][index] ?? null,
    removeItem: (key: string) => void values.delete(key),
    setItem: (key: string, value: string) => void values.set(key, value),
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const fetchMock = vi.fn();

beforeEach(() => {
  vi.stubGlobal("window", { localStorage: fakeStorage() });
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("api client authentication", () => {
  it("sends the stored bearer token only on mutating requests", async () => {
    setApiToken("  write-secret  ");
    fetchMock.mockResolvedValue(jsonResponse({ kill: true }));

    await cypApi.setKillSwitch(true);
    const [, mutateInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(new Headers(mutateInit.headers).get("Authorization")).toBe("Bearer write-secret");
    expect(new Headers(mutateInit.headers).get("Content-Type")).toBe("application/json");

    fetchMock.mockResolvedValue(jsonResponse({ status: "ok" }));
    await cypApi.health();
    const [, readInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(new Headers(readInit?.headers).get("Authorization")).toBeNull();
  });

  it("clears the token when set to blank", async () => {
    setApiToken("write-secret");
    setApiToken("   ");
    fetchMock.mockResolvedValue(jsonResponse({ kill: false }));

    await cypApi.setKillSwitch(false);
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(new Headers(init.headers).get("Authorization")).toBeNull();
  });
});

describe("api client error handling", () => {
  it("times out stalled API requests", async () => {
    vi.useFakeTimers();
    fetchMock.mockImplementation((_path: string, init: RequestInit) => new Promise((_resolve, reject) => {
      init.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
    }));
    const request = cypApi.health();
    const assertion = expect(request).rejects.toThrow("请求超时");
    await vi.advanceTimersByTimeAsync(30_000);
    await assertion;
  });

  it("surfaces the structured detail from error responses", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ detail: "窗口必须小于 bars" }, 422));
    await expect(
      cypApi.backtest({ bars: 10, window: 20 } as never),
    ).rejects.toThrow("窗口必须小于 bars");
  });

  it("falls back to the HTTP status for empty error bodies", async () => {
    fetchMock.mockResolvedValue(new Response("", { status: 503 }));
    await expect(cypApi.health()).rejects.toThrow("HTTP 503");
  });

  it("encodes query parameters", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ symbol: "BTC/USDT" }));
    await cypApi.market("BTC/USDT");
    const [url] = fetchMock.mock.calls[0] as [string];
    expect(url).toBe("/api/market?symbol=BTC%2FUSDT");
  });

  it("encodes repeated symbols for market history", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ venue: "okx", timeframe: "4h", series: [] }));
    await cypApi.marketHistory(["BTC/USDT:USDT", "ETH/USDT:USDT"], "4h", 42);
    const [url] = fetchMock.mock.calls[0] as [string];
    const parsed = new URL(url, "http://localhost");
    expect(parsed.pathname).toBe("/api/market/history");
    expect(parsed.searchParams.getAll("symbol")).toEqual(["BTC/USDT:USDT", "ETH/USDT:USDT"]);
    expect(parsed.searchParams.get("timeframe")).toBe("4h");
    expect(parsed.searchParams.get("limit")).toBe("42");
  });

  it("sends the selected symbol when starting an analysis run", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ run_id: "run-1", symbol: "ETH/USDT" }));

    await cypApi.runOnce("ETH/USDT");

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/run");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ symbol: "ETH/USDT" });
  });

  it("sends runtime mode changes through the settings endpoint", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ mode: "live" }));

    await cypApi.updateSettings({ mode: "live" });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/settings");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ mode: "live" });
  });

  it("persists the configured analysis watchlist", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ watchlist: ["BTC/USDT:USDT", "ETH/USDT:USDT"] }));

    await cypApi.updateSettings({ watchlist: ["BTC/USDT:USDT", "ETH/USDT:USDT"] });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/settings");
    expect(JSON.parse(String(init.body))).toEqual({ watchlist: ["BTC/USDT:USDT", "ETH/USDT:USDT"] });
  });
});
