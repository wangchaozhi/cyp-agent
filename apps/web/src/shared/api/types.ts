export type Numeric = string | number;
export type Side = "long" | "short" | "flat";
export type Verdict = "approved" | "downsized" | "rejected";
export type Instrument = "spot" | "perp";
export type EntryType = "market" | "limit" | "range";
export type MarginMode = "isolated" | "cross";
export type Stance = "bullish" | "bearish" | "neutral";
export type RuntimeMode = "paper" | "live";

export interface AutomationSettings {
  enabled: boolean;
  scan_enabled: boolean;
  entry_enabled: boolean;
  approval_enabled: boolean;
  exit_enabled: boolean;
  reverse_enabled: boolean;
  add_enabled: boolean;
  max_risk_score: number;
  max_quote: Numeric;
  min_entry_quote: Numeric;
  min_confidence: number;
  min_reward_risk: number;
  kelly_scale: number;
  add_min_confidence: number;
  add_min_profit_r: number;
  add_risk_decay: number;
  add_max_position_fraction: number;
  add_cooldown_minutes: number;
  max_adds_per_position: number;
  reverse_min_confidence: number;
  reverse_min_reward_risk: number;
  reverse_confirmations: number;
  reverse_signal_minutes: number;
  reverse_cooldown_minutes: number;
  max_reversals_per_day: number;
  ewma_lambda: number;
  volatility_multiplier: number;
  trail_activation_r: number;
  trail_giveback_r: number;
  profit_target_r: number;
  loss_cut_r: number;
  max_holding_minutes: number;
  time_stop_min_r: number;
  exit_confirmations: number;
  exit_min_samples: number;
}

export interface HealthStatus {
  ok: boolean;
  mode: RuntimeMode;
  display_mode?: string;
  execution_venue?: string;
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

export interface LeveragePlan {
  model: string;
  required_leverage: number;
  safe_max_leverage: number;
  selected_leverage: number;
  stop_fraction: Numeric;
  volatility_fraction: Numeric;
  required_liquidation_buffer: Numeric;
  margin_budget_quote: Numeric;
  estimated_margin_quote: Numeric;
  max_notional_quote: Numeric;
  downsized: boolean;
}

export interface AddOnPlan {
  model: string;
  existing_notional_quote: Numeric;
  existing_leverage: number;
  profit_r: number;
  add_index: number;
  max_adds: number;
  risk_decay: number;
  risk_fraction: number;
  max_position_fraction: number;
  recommended_notional_quote: Numeric;
  cooldown_minutes: number;
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
  leverage_plan?: LeveragePlan;
  add_on_plan?: AddOnPlan;
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
  liq_price?: Numeric | null;
  margin_mode?: MarginMode | null;
  chain?: string | null;
  tx_hash?: string | null;
  mark_price?: Numeric;
  notional?: Numeric;
  unrealized_pnl?: Numeric;
  unrealized_pnl_pct?: Numeric;
  margin_used?: Numeric | null;
  funding_rate?: Numeric | null;
}

export interface TradeRecord {
  client_id: string;
  run_id?: string;
  kind: "open" | "close";
  symbol: string;
  side: Exclude<Side, "flat">;
  instrument: Instrument;
  price: Numeric;
  size_base: Numeric;
  fee_quote: Numeric;
  pnl_quote: Numeric;
  ts: string;
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
  realized_pnl: Numeric;
  portfolio_cvar_quote?: Numeric | null;
  cvar_samples: number;
  margin_ratio?: Numeric | null;
  perp_notional?: Numeric;
  limits: {
    daily_dd: Numeric;
    weekly_dd: Numeric;
    total_dd: Numeric;
    max_leverage: Numeric;
    max_margin_pct: Numeric;
    leverage_step: Numeric;
    min_liq_buffer: Numeric;
    max_orders_per_hour: number;
    max_consecutive_losses: number;
    min_margin_ratio?: Numeric;
  };
  live_guard: {
    ok: boolean;
    reasons: string[];
  };
}

export interface RuntimeSettings {
  mode: RuntimeMode;
  approval: string;
  kill: boolean;
  allow_perp: boolean;
  execution_venue: string;
  data_source: string;
  llm_enabled: boolean;
  llm_provider: string;
  llm_model: string;
  llm_model_fast: string;
  llm_base_url: string | null;
  api_auth_enabled: boolean;
  cex_id: string;
  cex_trading_configured: boolean;
  okx: {
    configured: boolean;
    demo: boolean;
  };
  watchlist: string[];
  intervals: {
    scan: number;
    monitor: number;
  };
  runtime: {
    max_concurrency: number;
    log_level: string;
    autostart: boolean;
    persistence: "memory" | "file" | "postgres";
    ohlcv_archive_enabled: boolean;
    ohlcv_retention_days: number;
  };
  risk: {
    max_risk_per_trade: Numeric;
    max_position_pct: Numeric;
    max_gross_exposure: Numeric;
    max_symbol_concentration: Numeric;
    max_correlated_exposure: Numeric;
    max_cvar_pct: Numeric;
    max_orders_per_hour: number;
    max_slippage_bps: Numeric;
    max_leverage: Numeric;
    max_margin_pct: Numeric;
    leverage_step: Numeric;
    min_liq_buffer: Numeric;
    liq_stop_multiple: Numeric;
    liq_vol_multiple: Numeric;
    liq_reserve_pct: Numeric;
    force_isolated: boolean;
    min_margin_ratio: Numeric;
    daily_drawdown_limit: Numeric;
    weekly_drawdown_limit: Numeric;
    max_drawdown_limit: Numeric;
    max_consecutive_losses: number;
    approval_timeout_seconds: number;
  };
  budget: {
    max_iterations: number;
    max_tokens: number;
    max_cost_usd: number;
    max_wall_seconds: number;
  };
  automation: AutomationSettings;
  live_guard: {
    ok: boolean;
    reasons: string[];
  };
}

export interface RuntimeSettingsUpdate {
  mode?: RuntimeMode;
  watchlist?: string[];
  scan_interval?: number;
  llm_provider?: string;
  llm_model?: string;
  llm_model_fast?: string;
  llm_base_url?: string;
  anthropic_api_key?: string;
  deepseek_api_key?: string;
  automation?: Partial<AutomationSettings>;
}

export interface SymbolExposure {
  symbol: string;
  cluster: string;
  long: Numeric;
  short: Numeric;
}

export interface PortfolioSnapshot {
  equity: Numeric;
  n_positions: number;
  gross: Numeric;
  clusters: Record<"major" | "alt", Record<Exclude<Side, "flat">, Numeric>>;
  by_symbol: SymbolExposure[];
  correlated_limit: Numeric;
}

export interface MarketSnapshotInfo {
  symbol: string;
  tickers: Record<string, Numeric>;
  best_buy: { venue: string | null; price: Numeric | null };
  best_sell: { venue: string | null; price: Numeric | null };
  spread_bps: Numeric | null;
  funding_rates: Record<string, Numeric>;
  arb_hints: string[];
}

export interface MarketHistoryPoint {
  ts: string;
  close: Numeric;
}

export interface MarketHistorySeries {
  symbol: string;
  points: MarketHistoryPoint[];
}

export interface MarketHistoryResponse {
  venue: string;
  timeframe: string;
  series: MarketHistorySeries[];
}

export interface BacktestRequest {
  symbol?: string;
  bars: number;
  window: number;
  seed: number;
  drift: number;
  vol: number;
  data?: "synthetic" | "cex";
  timeframe?: string;
  fee_rate: number;
  slippage_bps: number;
  spread_bps: number;
  funding_rate: number;
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
  total_costs: number;
}

export interface BacktestTrade {
  side: Exclude<Side, "flat">;
  entry: number;
  exit: number;
  pnl: number;
  bar_in: number;
  bar_out: number;
  costs: number;
}

export interface BacktestReport {
  symbol: string;
  n_bars: number;
  metrics: BacktestMetrics;
  trades: BacktestTrade[];
  equity_curve: number[];
  lessons: string[];
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
  kind: "entry" | "close";
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
  position_side?: Side;
  proposal_side?: Side;
  side?: Side;
  add_on?: {
    allowed: boolean;
    reason: string;
    plan?: AddOnPlan;
  };
  reversal?: {
    ready: boolean;
    side: Side;
    confirmations: number;
    required: number;
    reversals_today: number;
    cooldown_until?: string;
    reason: string;
  };
  exit_decision?: {
    trigger: boolean;
    reason: string;
    current_r: number;
    peak_r: number;
    volatility_r: number;
    trail_floor_r: number;
    samples: number;
    confirmations: number;
    holding: number;
  };
  execution?: ExecutionResult;
  review?: TradeReview;
  error?: string;
  status?: string;
  on?: boolean;
  positions?: unknown;
  trace?: unknown;
}

export type ApprovalRequest =
  | { decision: "approve"; note?: string; operator?: string }
  | { decision: "reject"; note?: string; operator?: string }
  | { decision: "modify"; size: number; note?: string; operator?: string };

export interface RunMetricsSnapshot {
  runs: number;
  executed: number;
  rejected: number;
  not_approved: number;
  no_trade: number;
  errors: number;
  avg_slippage_bps: number;
  approval_rate: number;
  order_success_rate: number;
  slippage_hist_bps: Record<string, number>;
  approval_latency: { avg_s: number; max_s: number; n: number };
}

export interface MetricsSnapshot {
  runs: RunMetricsSnapshot;
  llm: Record<string, unknown>;
  runtime: {
    scan_cycles: number;
    scan_errors: number;
    monitor_cycles: number;
    monitor_errors: number;
    reconcile_attempts: number;
    reconcile_failures: number;
    alerts: number;
    ohlcv_queued: number;
    ohlcv_saved: number;
    ohlcv_dropped: number;
    ohlcv_errors: number;
    ohlcv_pruned: number;
    ohlcv_repair_runs: number;
    ohlcv_backfilled: number;
  };
}
