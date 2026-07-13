import type {
  ApprovalRequest,
  BacktestReport,
  BacktestRequest,
  ExecutionResult,
  HealthStatus,
  MarketHistoryResponse,
  MarketSnapshotInfo,
  MetricsSnapshot,
  PendingApproval,
  PortfolioSnapshot,
  Position,
  RiskSnapshot,
  RuntimeSettings,
  RuntimeSettingsUpdate,
  TradeRecord,
  VenueInfo,
} from "./types";

const apiTokenStorageKey = "cyp-agent.api-token";

function storedApiToken(): string {
  try {
    return window.localStorage.getItem(apiTokenStorageKey)?.trim() ?? "";
  } catch {
    return "";
  }
}

export function setApiToken(value: string): void {
  try {
    const token = value.trim();
    if (token) window.localStorage.setItem(apiTokenStorageKey, token);
    else window.localStorage.removeItem(apiTokenStorageKey);
  } catch {
    // Storage can be unavailable in hardened/private browser contexts. The
    // request will fail closed when the server requires authentication.
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const method = (init?.method ?? "GET").toUpperCase();
  if (!["GET", "HEAD", "OPTIONS"].includes(method) && !headers.has("Authorization")) {
    const token = storedApiToken();
    if (token) headers.set("Authorization", `Bearer ${token}`);
  }

  const response = await fetch(path, { ...init, headers });
  if (!response.ok) {
    // The body can only be consumed once, so read it as text first and only
    // then try to extract a structured error detail from it.
    let detail = response.statusText;
    try {
      const text = await response.text();
      detail = text || detail;
      const body = JSON.parse(text) as { detail?: string };
      if (body.detail) detail = body.detail;
    } catch {
      // Keep the best detail collected so far.
    }
    throw new Error(detail || `HTTP ${response.status}`);
  }

  return response.json() as Promise<T>;
}

export const cypApi = {
  health: () => request<HealthStatus>("/api/health"),
  venues: () => request<VenueInfo[]>("/api/venues"),
  settings: () => request<RuntimeSettings>("/api/settings"),
  updateSettings: (payload: RuntimeSettingsUpdate) =>
    request<RuntimeSettings>("/api/settings", {
      method: "POST",
      body: JSON.stringify(payload),
    }),
  pending: () => request<PendingApproval[]>("/api/pending"),
  positions: () => request<Position[]>("/api/positions"),
  trades: () => request<TradeRecord[]>("/api/trades"),
  closePosition: (payload: { symbol: string; instrument: string }) =>
    request<ExecutionResult>("/api/positions/close", {
      method: "POST",
      body: JSON.stringify(payload),
    }),
  risk: () => request<RiskSnapshot>("/api/risk"),
  market: (symbol?: string) =>
    request<MarketSnapshotInfo>(`/api/market${symbol ? `?symbol=${encodeURIComponent(symbol)}` : ""}`),
  marketHistory: (symbols: string[], timeframe: string, limit: number) => {
    const params = new URLSearchParams({ timeframe, limit: String(limit) });
    symbols.forEach((symbol) => params.append("symbol", symbol));
    return request<MarketHistoryResponse>(`/api/market/history?${params.toString()}`);
  },
  metrics: () => request<MetricsSnapshot>("/api/metrics"),
  portfolio: () => request<PortfolioSnapshot>("/api/portfolio"),
  backtest: (payload: BacktestRequest) =>
    request<BacktestReport>("/api/backtest", {
      method: "POST",
      body: JSON.stringify(payload),
    }),
  runOnce: () =>
    request<{ run_id: string; symbol: string }>("/api/run", {
      method: "POST",
      body: JSON.stringify({}),
    }),
  setKillSwitch: (on: boolean) =>
    request<{ kill: boolean }>("/api/killswitch", {
      method: "POST",
      body: JSON.stringify({ on }),
    }),
  decideApproval: (runId: string, payload: ApprovalRequest) =>
    request<{ ok: boolean }>(`/api/approvals/${runId}`, {
      method: "POST",
      body: JSON.stringify(payload),
    }),
};
