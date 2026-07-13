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
  ReadinessStatus,
  RiskSnapshot,
  RuntimeSettings,
  RuntimeSettingsUpdate,
  TradeRecord,
	TokenUsageReport,
  VenueInfo,
} from "./types";

const apiTokenStorageKey = "cyp-agent.api-token";
const apiTimeoutMs = 30_000;

function storedApiToken(): string {
  try {
    const current = window.sessionStorage.getItem(apiTokenStorageKey)?.trim() ?? "";
    if (current) return current;
    // Migrate older installations once, then remove the persistent browser copy.
    const legacy = window.localStorage.getItem(apiTokenStorageKey)?.trim() ?? "";
    window.localStorage.removeItem(apiTokenStorageKey);
    if (legacy) window.sessionStorage.setItem(apiTokenStorageKey, legacy);
    return legacy;
  } catch {
    return "";
  }
}

export function setApiToken(value: string): void {
  try {
    const token = value.trim();
    if (token) window.sessionStorage.setItem(apiTokenStorageKey, token);
    else window.sessionStorage.removeItem(apiTokenStorageKey);
    window.localStorage.removeItem(apiTokenStorageKey);
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

  const controller = new AbortController();
  const abortFromCaller = () => controller.abort(init?.signal?.reason);
  if (init?.signal?.aborted) abortFromCaller();
  else init?.signal?.addEventListener("abort", abortFromCaller, { once: true });
  const timeout = globalThis.setTimeout(() => controller.abort("timeout"), apiTimeoutMs);
  let response: Response;
  try {
    response = await fetch(path, { ...init, headers, signal: controller.signal });
  } catch (error) {
    if (controller.signal.aborted && !init?.signal?.aborted) {
      throw new Error("请求超时，请检查后端或网络状态");
    }
    throw error;
  } finally {
    globalThis.clearTimeout(timeout);
    init?.signal?.removeEventListener("abort", abortFromCaller);
  }
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
  ready: () => request<ReadinessStatus>("/api/ready"),
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
	tokenUsage: (days = 7, bucket?: "hour" | "day", limit = 50) => {
		const params = new URLSearchParams({ days: String(days), limit: String(limit) });
		if (bucket) params.set("bucket", bucket);
		return request<TokenUsageReport>(`/api/token-usage?${params.toString()}`);
	},
  portfolio: () => request<PortfolioSnapshot>("/api/portfolio"),
  backtest: (payload: BacktestRequest) =>
    request<BacktestReport>("/api/backtest", {
      method: "POST",
      body: JSON.stringify(payload),
    }),
  runOnce: (symbol: string) =>
    request<{ run_id: string; symbol: string }>("/api/run", {
      method: "POST",
      body: JSON.stringify({ symbol }),
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
