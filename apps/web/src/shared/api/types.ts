export type Numeric = string | number;
export type Side = "long" | "short" | "flat";
export type Verdict = "approved" | "downsized" | "rejected";
export type Instrument = "spot" | "perp";
export type EntryType = "market" | "limit" | "range";
export type MarginMode = "isolated" | "cross";
export type Stance = "bullish" | "bearish" | "neutral";

export interface HealthStatus {
  ok: boolean;
  mode: string;
  llm: boolean;
  kill: boolean;
}

export interface VenueInfo {
  id: string;
  kind: string;
  configured: boolean;
  spot: boolean;
  perp: boolean;
  native_protective_orders: boolean;
  read_only: boolean;
}

export interface PricePlan {
  type: EntryType;
  price?: Numeric | null;
  low?: Numeric | null;
  high?: Numeric | null;
}

export interface TradeProposal {
  symbol: string;
  venue: string;
  side: Side;
  instrument: Instrument;
  size_quote: Numeric;
  leverage: number;
  margin_mode: MarginMode;
  entry: PricePlan;
  stop_loss?: Numeric | null;
  take_profit: Numeric[];
  confidence: number;
  thesis: string;
  supporting_reports: string[];
}

export interface RiskAssessment {
  verdict: Verdict;
  hard_violations: string[];
  adjusted_size_quote?: Numeric | null;
  llm_notes: string;
  risk_score: number;
  llm_reviewed: boolean;
}

export interface PendingApproval {
  run_id: string;
  proposal: TradeProposal;
  assessment: RiskAssessment;
}

export interface Position {
  symbol: string;
  venue: string;
  side: Exclude<Side, "flat">;
  instrument: Instrument;
  size_base: Numeric;
  entry_price: Numeric;
  leverage: number;
}

export interface RiskSnapshot {
  mode: string;
  kill: boolean;
  equity: Numeric;
  drawdown: {
    daily: Numeric;
    weekly: Numeric;
    total: Numeric;
  };
  orders_last_hour: number;
  consecutive_losses: number;
  limits: {
    daily_dd: Numeric;
    weekly_dd: Numeric;
    total_dd: Numeric;
    max_leverage: Numeric;
    max_orders_per_hour: number;
    max_consecutive_losses: number;
  };
  live_guard: {
    ok: boolean;
    reasons: string[];
  };
}

export interface PortfolioSnapshot {
  equity: Numeric;
  n_positions: number;
  gross: Numeric;
  clusters: Record<"major" | "alt", Record<Exclude<Side, "flat">, Numeric>>;
  correlated_limit: Numeric;
}

export interface BacktestRequest {
  symbol?: string;
  bars: number;
  window: number;
  seed: number;
  drift: number;
  vol: number;
}

export interface BacktestMetrics {
  initial_equity: number;
  final_equity: number;
  total_return: number;
  max_drawdown: number;
  sharpe: number;
  n_trades: number;
  win_rate: number;
  profit_factor: number | null;
}

export interface BacktestTrade {
  side: Exclude<Side, "flat">;
  entry: number;
  exit: number;
  pnl: number;
  bar_in: number;
  bar_out: number;
}

export interface BacktestReport {
  symbol: string;
  n_bars: number;
  metrics: BacktestMetrics;
  trades: BacktestTrade[];
  equity_curve: number[];
  params: Required<BacktestRequest>;
}

export interface Signal {
  name: string;
  value: string;
  note: string;
}

export interface AnalystReport {
  agent: string;
  stance: Stance;
  confidence: number;
  signals: Signal[];
  rationale: string;
  degraded: boolean;
}

export interface ExecutionResult {
  client_id: string;
  order_id?: string | null;
  status: string;
  filled_base: Numeric;
  avg_price?: Numeric | null;
  fee_quote: Numeric;
  slippage_bps?: Numeric | null;
  protective_orders: unknown[];
  error?: string | null;
}

export interface ApprovalDecision {
  decision: "approve" | "reject" | "modify";
  modified?: TradeProposal | null;
  operator: string;
  ts: string;
  note: string;
}

export interface TradeReview {
  symbol: string;
  proposal_ref: string;
  score: number;
  pnl_quote: Numeric;
  slippage_bps?: Numeric | null;
  lessons: string[];
  notes: string;
  ts: string;
}

export interface DashboardEvent {
  type: string;
  run_id: string;
  ts: string;
  symbol?: string;
  bars?: number;
  reports?: AnalystReport[];
  proposal?: TradeProposal;
  assessment?: RiskAssessment;
  decision?: ApprovalDecision;
  execution?: ExecutionResult;
  review?: TradeReview;
  error?: string;
  status?: string;
  on?: boolean;
  positions?: unknown;
  trace?: unknown;
}

export type ApprovalRequest =
  | { decision: "approve"; note?: string }
  | { decision: "reject"; note?: string }
  | { decision: "modify"; size: number; note?: string };
