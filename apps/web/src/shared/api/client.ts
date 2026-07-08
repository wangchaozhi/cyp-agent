import type {
  ApprovalRequest,
  BacktestReport,
  BacktestRequest,
  HealthStatus,
  PendingApproval,
  PortfolioSnapshot,
  Position,
  RiskSnapshot,
  VenueInfo,
} from "./types";

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const response = await fetch(path, { ...init, headers });
  if (!response.ok) {
    let detail = response.statusText;
    try {
      const body = (await response.json()) as { detail?: string };
      detail = body.detail || detail;
    } catch {
      detail = await response.text();
    }
    throw new Error(detail || `HTTP ${response.status}`);
  }

  return response.json() as Promise<T>;
}

export const cypApi = {
  health: () => request<HealthStatus>("/api/health"),
  venues: () => request<VenueInfo[]>("/api/venues"),
  pending: () => request<PendingApproval[]>("/api/pending"),
  positions: () => request<Position[]>("/api/positions"),
  risk: () => request<RiskSnapshot>("/api/risk"),
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
