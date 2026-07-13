package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

// LiveExecutionSupported is deliberately false in the first Go slice. This is
// a compile-time safety rail: credentials and CYP_LIVE_ACK cannot enable live
// order placement until the persistent order state machine and reconciliation
// gates have shipped.
const LiveExecutionSupported = false

type Secret string

func (s Secret) Configured() bool { return strings.TrimSpace(string(s)) != "" }
func (s Secret) Reveal() string   { return string(s) }
func (s Secret) String() string {
	if !s.Configured() {
		return ""
	}
	return "[REDACTED]"
}
func (s Secret) GoString() string             { return s.String() }
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

type RiskConfig struct {
	MaxRiskPerTrade        contracts.Decimal
	MaxPositionPct         contracts.Decimal
	MaxGrossExposure       contracts.Decimal
	MaxSymbolConcentration contracts.Decimal
	MaxCorrelatedExposure  contracts.Decimal
	MaxCVARPct             contracts.Decimal
	MaxOrdersPerHour       int
	MaxSlippageBPS         contracts.Decimal
	MaxLeverage            contracts.Decimal
	MaxMarginPct           contracts.Decimal
	LeverageStep           contracts.Decimal
	MinLiqBuffer           contracts.Decimal
	LiqStopMultiple        contracts.Decimal
	LiqVolMultiple         contracts.Decimal
	LiqReservePct          contracts.Decimal
	ForceIsolated          bool
	MinMarginRatio         contracts.Decimal
	MaxPriceImpact         contracts.Decimal
	MaxGasGwei             *contracts.Decimal
	MaxGasQuote            contracts.Decimal
	MinPoolTVL             contracts.Decimal
	ContractWhitelist      string
	RequirePrivateMempool  bool
	DailyDrawdownLimit     contracts.Decimal
	WeeklyDrawdownLimit    contracts.Decimal
	MaxDrawdownLimit       contracts.Decimal
	MaxConsecutiveLosses   int
	ApprovalTimeoutSeconds int
}

func (r RiskConfig) ContractWhitelistSet() map[string]struct{} {
	result := make(map[string]struct{})
	for _, item := range strings.Split(r.ContractWhitelist, ",") {
		if value := strings.ToLower(strings.TrimSpace(item)); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

type BudgetConfig struct {
	MaxIterations      int
	MaxTokens          int
	MaxCostUSD         float64
	MaxWallSeconds     int
	DailyTokenBudget   int
	DailyCostBudgetUSD float64
}

// AutomationConfig contains non-secret, runtime-mutable strategy controls.
// Native protective orders are intentionally outside this switch.
type AutomationConfig struct {
	Enabled                bool              `json:"enabled"`
	ScanEnabled            bool              `json:"scan_enabled"`
	EntryEnabled           bool              `json:"entry_enabled"`
	ApprovalEnabled        bool              `json:"approval_enabled"`
	ExitEnabled            bool              `json:"exit_enabled"`
	ReverseEnabled         bool              `json:"reverse_enabled"`
	AddEnabled             bool              `json:"add_enabled"`
	MaxRiskScore           float64           `json:"max_risk_score"`
	MaxQuote               contracts.Decimal `json:"max_quote"`
	MinEntryQuote          contracts.Decimal `json:"min_entry_quote"`
	MinConfidence          float64           `json:"min_confidence"`
	MinRewardRisk          float64           `json:"min_reward_risk"`
	KellyScale             float64           `json:"kelly_scale"`
	AddMinConfidence       float64           `json:"add_min_confidence"`
	AddMinProfitR          float64           `json:"add_min_profit_r"`
	AddRiskDecay           float64           `json:"add_risk_decay"`
	AddMaxPositionFraction float64           `json:"add_max_position_fraction"`
	AddCooldownMinutes     int               `json:"add_cooldown_minutes"`
	MaxAddsPerPosition     int               `json:"max_adds_per_position"`
	ReverseMinConfidence   float64           `json:"reverse_min_confidence"`
	ReverseMinRewardRisk   float64           `json:"reverse_min_reward_risk"`
	ReverseConfirmations   int               `json:"reverse_confirmations"`
	ReverseSignalMinutes   int               `json:"reverse_signal_minutes"`
	ReverseCooldownMins    int               `json:"reverse_cooldown_minutes"`
	MaxReversalsPerDay     int               `json:"max_reversals_per_day"`
	EWMALambda             float64           `json:"ewma_lambda"`
	VolatilityMultiplier   float64           `json:"volatility_multiplier"`
	TrailActivationR       float64           `json:"trail_activation_r"`
	TrailGivebackR         float64           `json:"trail_giveback_r"`
	ProfitTargetR          float64           `json:"profit_target_r"`
	LossCutR               float64           `json:"loss_cut_r"`
	MaxHoldingMinutes      int               `json:"max_holding_minutes"`
	TimeStopMinR           float64           `json:"time_stop_min_r"`
	ExitConfirmations      int               `json:"exit_confirmations"`
	ExitMinSamples         int               `json:"exit_min_samples"`
}

type Settings struct {
	Mode           string
	Approval       string
	AutoSymbols    string
	Kill           bool
	AllowPerp      bool
	ExecutionVenue string
	DataSource     string

	LLMProvider     string
	LLMModel        string
	LLMModelFast    string
	LLMBaseURL      string
	AnthropicAPIKey Secret
	DeepSeekAPIKey  Secret

	CEXID            string
	BinanceAPIKey    Secret
	BinanceAPISecret Secret
	LiveAck          bool
	OKXAPIKey        Secret
	OKXAPISecret     Secret
	OKXPassword      Secret
	OKXDemo          bool

	AlertWebhook   string
	EVMRPCURL      string
	Signer         string
	OnchainDataAPI string

	RuntimeAutostart bool
	ScanInterval     int
	MonitorInterval  int
	Watchlist        string
	MaxConcurrency   int

	DBURL                   string
	Persistence             string
	StateFile               string
	OHLCVArchiveEnabled     bool
	OHLCVRetentionDays      int
	TokenUsageEnabled       bool
	TokenUsageRetentionDays int
	TokenUsageTimezone      string
	LogLevel                string
	APIToken                Secret
	Risk                    RiskConfig
	Budget                  BudgetConfig
	Automation              AutomationConfig
}

func DefaultRiskConfig() RiskConfig {
	return RiskConfig{
		MaxRiskPerTrade:        contracts.MustDecimal("0.01"),
		MaxPositionPct:         contracts.MustDecimal("0.20"),
		MaxGrossExposure:       contracts.MustDecimal("1.00"),
		MaxSymbolConcentration: contracts.MustDecimal("0.30"),
		MaxCorrelatedExposure:  contracts.MustDecimal("0.50"),
		MaxCVARPct:             contracts.MustDecimal("0.03"),
		MaxOrdersPerHour:       10,
		MaxSlippageBPS:         contracts.MustDecimal("30"),
		MaxLeverage:            contracts.MustDecimal("3"),
		MaxMarginPct:           contracts.MustDecimal("0.10"),
		LeverageStep:           contracts.MustDecimal("1"),
		MinLiqBuffer:           contracts.MustDecimal("0.30"),
		LiqStopMultiple:        contracts.MustDecimal("2"),
		LiqVolMultiple:         contracts.MustDecimal("3"),
		LiqReservePct:          contracts.MustDecimal("0.02"),
		ForceIsolated:          true,
		MinMarginRatio:         contracts.MustDecimal("0.05"),
		MaxPriceImpact:         contracts.MustDecimal("0.01"),
		MaxGasQuote:            contracts.MustDecimal("20"),
		MinPoolTVL:             contracts.MustDecimal("1000000"),
		RequirePrivateMempool:  true,
		DailyDrawdownLimit:     contracts.MustDecimal("0.03"),
		WeeklyDrawdownLimit:    contracts.MustDecimal("0.08"),
		MaxDrawdownLimit:       contracts.MustDecimal("0.15"),
		MaxConsecutiveLosses:   4,
		ApprovalTimeoutSeconds: 1800,
	}
}

func DefaultSettings() Settings {
	return Settings{
		Mode:                    "paper",
		Approval:                "dashboard",
		ExecutionVenue:          "paper",
		DataSource:              "synthetic",
		LLMProvider:             "anthropic",
		LLMModel:                "claude-opus-4-8",
		LLMModelFast:            "claude-haiku-4-5-20251001",
		CEXID:                   "binance",
		OKXDemo:                 true,
		Signer:                  "keystore",
		ScanInterval:            600,
		MonitorInterval:         5,
		Watchlist:               "BTC/USDT",
		MaxConcurrency:          2,
		DBURL:                   "postgresql://cyp:cyp@localhost:5433/cyp",
		Persistence:             "file",
		StateFile:               "data/cyp-state.json",
		OHLCVArchiveEnabled:     true,
		OHLCVRetentionDays:      730,
		TokenUsageEnabled:       true,
		TokenUsageRetentionDays: 90,
		TokenUsageTimezone:      "Asia/Shanghai",
		LogLevel:                "INFO",
		Automation: AutomationConfig{
			Enabled: true, ScanEnabled: true, EntryEnabled: true, ApprovalEnabled: true,
			ExitEnabled: true, ReverseEnabled: true, AddEnabled: true,
			MaxRiskScore: 0.5, MaxQuote: contracts.MustDecimal("200"),
			MinEntryQuote: contracts.MustDecimal("20"), MinConfidence: 0.65,
			MinRewardRisk: 1.5, KellyScale: 0.25,
			AddMinConfidence: 0.75, AddMinProfitR: 0.5, AddRiskDecay: 0.5,
			AddMaxPositionFraction: 0.5, AddCooldownMinutes: 60, MaxAddsPerPosition: 2,
			ReverseMinConfidence: 0.75, ReverseMinRewardRisk: 2,
			ReverseConfirmations: 2, ReverseSignalMinutes: 30,
			ReverseCooldownMins: 60, MaxReversalsPerDay: 2, EWMALambda: 0.94,
			VolatilityMultiplier: 3, TrailActivationR: 1, TrailGivebackR: 0.5,
			ProfitTargetR: 1.5, LossCutR: 0.5,
			MaxHoldingMinutes: 360, TimeStopMinR: 0, ExitConfirmations: 2, ExitMinSamples: 8,
		},
		Risk: DefaultRiskConfig(),
		Budget: BudgetConfig{
			MaxIterations:      20,
			MaxTokens:          200_000,
			MaxCostUSD:         2.0,
			MaxWallSeconds:     300,
			DailyTokenBudget:   2_000_000,
			DailyCostBudgetUSD: 50,
		},
	}
}

func (s Settings) LLMEnabled() bool {
	if s.LLMProvider == "deepseek" {
		return s.DeepSeekAPIKey.Configured()
	}
	return s.AnthropicAPIKey.Configured()
}

func (s Settings) OKXConfigured() bool {
	return s.OKXAPIKey.Configured() && s.OKXAPISecret.Configured() && s.OKXPassword.Configured()
}

func (s Settings) CEXTradingConfigured() bool {
	if s.CEXID == "okx" {
		return s.OKXConfigured()
	}
	return s.BinanceAPIKey.Configured() && s.BinanceAPISecret.Configured()
}

func splitCSV(value string) []string {
	items := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func (s Settings) WatchlistSymbols() []string { return splitCSV(s.Watchlist) }
func (s Settings) AutoSymbolsList() []string  { return splitCSV(s.AutoSymbols) }

type LiveGuardReport struct {
	OK      bool     `json:"ok"`
	Reasons []string `json:"reasons"`
}

func (s Settings) LiveGuard() LiveGuardReport {
	reasons := make([]string, 0, 4)
	if s.Mode == "live" {
		if !s.CEXTradingConfigured() {
			reasons = append(reasons, "缺少交易所 API Key，无法实盘（保持只读）")
		}
		if !s.LiveAck {
			reasons = append(reasons, "未确认实盘：请设置 CYP_LIVE_ACK=1")
		}
		if s.Kill {
			reasons = append(reasons, "Kill Switch 开启，禁止实盘")
		}
		// This reason remains unconditional: production execution is not
		// unlocked by credentials or acknowledgement.
		reasons = append(reasons, "Go 首版硬禁实盘执行；仅允许 Paper 或 OKX Demo")
		return LiveGuardReport{OK: false, Reasons: reasons}
	}
	if s.ExecutionVenue == "paper" {
		return LiveGuardReport{OK: true, Reasons: []string{}}
	}
	if s.ExecutionVenue == "okx" {
		if !s.OKXDemo {
			reasons = append(reasons, "OKX 执行仅允许 Demo；请设置 CYP_OKX_DEMO=true")
		}
		if !s.OKXConfigured() {
			reasons = append(reasons, "OKX Demo 缺少 Demo API Key、Secret 或 Passphrase")
		}
		return LiveGuardReport{OK: len(reasons) == 0, Reasons: reasons}
	}
	reasons = append(reasons, "当前仅允许 Paper 或 OKX Demo 执行")
	return LiveGuardReport{OK: false, Reasons: reasons}
}

func (s Settings) LiveExecutionAllowed() bool { return false }

// OKXDemoExecutionConfigured proves the selected execution path is the
// simulated OKX environment and has the complete Demo credential tuple.
func (s Settings) OKXDemoExecutionConfigured() bool {
	return s.Mode == "paper" && s.ExecutionVenue == "okx" && s.OKXDemo && s.OKXConfigured()
}

// NewExecutionConfigured deliberately excludes the Kill Switch so readiness
// can report configuration and operational permission independently.
func (s Settings) NewExecutionConfigured() bool {
	return s.Mode == "paper" && (s.ExecutionVenue == "paper" || s.OKXDemoExecutionConfigured())
}

// NewPositionAllowed applies the Kill Switch to the supported execution
// paths. Reduce-only and close paths must not use this helper.
func (s Settings) NewPositionAllowed() bool { return s.NewExecutionConfigured() && !s.Kill }

// NewPaperPositionAllowed describes only the first Go slice. Kill Switch does
// not disable reduce-only/close paths, so callers must not use this for exits.
func (s Settings) NewPaperPositionAllowed() bool {
	return s.Mode == "paper" && s.ExecutionVenue == "paper" && !s.Kill
}

type SettingsSnapshot struct {
	Mode                 string           `json:"mode"`
	Approval             string           `json:"approval"`
	Kill                 bool             `json:"kill"`
	AllowPerp            bool             `json:"allow_perp"`
	ExecutionVenue       string           `json:"execution_venue"`
	DataSource           string           `json:"data_source"`
	LLMEnabled           bool             `json:"llm_enabled"`
	LLMProvider          string           `json:"llm_provider"`
	LLMModel             string           `json:"llm_model"`
	LLMModelFast         string           `json:"llm_model_fast"`
	LLMBaseURL           *string          `json:"llm_base_url"`
	CEXID                string           `json:"cex_id"`
	CEXTradingConfigured bool             `json:"cex_trading_configured"`
	OKX                  OKXSnapshot      `json:"okx"`
	Watchlist            []string         `json:"watchlist"`
	Intervals            IntervalSnapshot `json:"intervals"`
	Runtime              RuntimeSnapshot  `json:"runtime"`
	APIAuthEnabled       bool             `json:"api_auth_enabled"`
	Risk                 RiskSnapshot     `json:"risk"`
	Budget               BudgetSnapshot   `json:"budget"`
	Automation           AutomationConfig `json:"automation"`
	LiveGuard            LiveGuardReport  `json:"live_guard"`
}

type OKXSnapshot struct {
	Configured bool `json:"configured"`
	Demo       bool `json:"demo"`
}

type IntervalSnapshot struct {
	Scan    int `json:"scan"`
	Monitor int `json:"monitor"`
}

type RuntimeSnapshot struct {
	MaxConcurrency          int    `json:"max_concurrency"`
	LogLevel                string `json:"log_level"`
	Autostart               bool   `json:"autostart"`
	Persistence             string `json:"persistence"`
	OHLCVArchiveEnabled     bool   `json:"ohlcv_archive_enabled"`
	OHLCVRetentionDays      int    `json:"ohlcv_retention_days"`
	TokenUsageEnabled       bool   `json:"token_usage_enabled"`
	TokenUsageRetentionDays int    `json:"token_usage_retention_days"`
	TokenUsageTimezone      string `json:"token_usage_timezone"`
}

type RiskSnapshot struct {
	MaxRiskPerTrade        contracts.Decimal `json:"max_risk_per_trade"`
	MaxPositionPct         contracts.Decimal `json:"max_position_pct"`
	MaxGrossExposure       contracts.Decimal `json:"max_gross_exposure"`
	MaxSymbolConcentration contracts.Decimal `json:"max_symbol_concentration"`
	MaxCorrelatedExposure  contracts.Decimal `json:"max_correlated_exposure"`
	MaxCVARPct             contracts.Decimal `json:"max_cvar_pct"`
	MaxOrdersPerHour       int               `json:"max_orders_per_hour"`
	MaxSlippageBPS         contracts.Decimal `json:"max_slippage_bps"`
	MaxLeverage            contracts.Decimal `json:"max_leverage"`
	MaxMarginPct           contracts.Decimal `json:"max_margin_pct"`
	LeverageStep           contracts.Decimal `json:"leverage_step"`
	MinLiqBuffer           contracts.Decimal `json:"min_liq_buffer"`
	LiqStopMultiple        contracts.Decimal `json:"liq_stop_multiple"`
	LiqVolMultiple         contracts.Decimal `json:"liq_vol_multiple"`
	LiqReservePct          contracts.Decimal `json:"liq_reserve_pct"`
	ForceIsolated          bool              `json:"force_isolated"`
	MinMarginRatio         contracts.Decimal `json:"min_margin_ratio"`
	DailyDrawdownLimit     contracts.Decimal `json:"daily_drawdown_limit"`
	WeeklyDrawdownLimit    contracts.Decimal `json:"weekly_drawdown_limit"`
	MaxDrawdownLimit       contracts.Decimal `json:"max_drawdown_limit"`
	MaxConsecutiveLosses   int               `json:"max_consecutive_losses"`
	ApprovalTimeoutSeconds int               `json:"approval_timeout_seconds"`
}

type BudgetSnapshot struct {
	MaxIterations      int     `json:"max_iterations"`
	MaxTokens          int     `json:"max_tokens"`
	MaxCostUSD         float64 `json:"max_cost_usd"`
	MaxWallSeconds     int     `json:"max_wall_seconds"`
	DailyTokenBudget   int     `json:"daily_token_budget"`
	DailyCostBudgetUSD float64 `json:"daily_cost_budget_usd"`
}

func optionalString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copy := value
	return &copy
}

func (s Settings) Snapshot() SettingsSnapshot {
	r := s.Risk
	return SettingsSnapshot{
		Mode: s.Mode, Approval: s.Approval, Kill: s.Kill, AllowPerp: s.AllowPerp,
		ExecutionVenue: s.ExecutionVenue, DataSource: s.DataSource,
		LLMEnabled: s.LLMEnabled(), LLMProvider: s.LLMProvider, LLMModel: s.LLMModel,
		LLMModelFast: s.LLMModelFast, LLMBaseURL: optionalString(s.LLMBaseURL),
		CEXID: s.CEXID, CEXTradingConfigured: s.CEXTradingConfigured(),
		OKX:       OKXSnapshot{Configured: s.OKXConfigured(), Demo: s.OKXDemo},
		Watchlist: s.WatchlistSymbols(),
		Intervals: IntervalSnapshot{Scan: s.ScanInterval, Monitor: s.MonitorInterval},
		Runtime: RuntimeSnapshot{MaxConcurrency: s.MaxConcurrency, LogLevel: s.LogLevel,
			Autostart: s.RuntimeAutostart, Persistence: s.Persistence,
			OHLCVArchiveEnabled: s.OHLCVArchiveEnabled, OHLCVRetentionDays: s.OHLCVRetentionDays,
			TokenUsageEnabled: s.TokenUsageEnabled, TokenUsageRetentionDays: s.TokenUsageRetentionDays,
			TokenUsageTimezone: s.TokenUsageTimezone},
		APIAuthEnabled: s.APIToken.Configured(),
		Risk: RiskSnapshot{
			MaxRiskPerTrade: r.MaxRiskPerTrade, MaxPositionPct: r.MaxPositionPct,
			MaxGrossExposure: r.MaxGrossExposure, MaxSymbolConcentration: r.MaxSymbolConcentration,
			MaxCorrelatedExposure: r.MaxCorrelatedExposure, MaxCVARPct: r.MaxCVARPct,
			MaxOrdersPerHour: r.MaxOrdersPerHour, MaxSlippageBPS: r.MaxSlippageBPS,
			MaxLeverage: r.MaxLeverage, MaxMarginPct: r.MaxMarginPct,
			LeverageStep: r.LeverageStep, MinLiqBuffer: r.MinLiqBuffer,
			LiqStopMultiple: r.LiqStopMultiple, LiqVolMultiple: r.LiqVolMultiple,
			LiqReservePct: r.LiqReservePct,
			ForceIsolated: r.ForceIsolated, MinMarginRatio: r.MinMarginRatio,
			DailyDrawdownLimit: r.DailyDrawdownLimit, WeeklyDrawdownLimit: r.WeeklyDrawdownLimit,
			MaxDrawdownLimit: r.MaxDrawdownLimit, MaxConsecutiveLosses: r.MaxConsecutiveLosses,
			ApprovalTimeoutSeconds: r.ApprovalTimeoutSeconds,
		},
		Budget: BudgetSnapshot{MaxIterations: s.Budget.MaxIterations, MaxTokens: s.Budget.MaxTokens,
			MaxCostUSD: s.Budget.MaxCostUSD, MaxWallSeconds: s.Budget.MaxWallSeconds,
			DailyTokenBudget: s.Budget.DailyTokenBudget, DailyCostBudgetUSD: s.Budget.DailyCostBudgetUSD},
		Automation: s.Automation,
		LiveGuard:  s.LiveGuard(),
	}
}

// MarshalJSON and String intentionally expose only the redacted runtime
// snapshot. In particular DB credentials and API keys cannot leak through
// ordinary structured logging.
func (s Settings) MarshalJSON() ([]byte, error) { return json.Marshal(s.Snapshot()) }
func (s Settings) String() string {
	encoded, err := json.Marshal(s.Snapshot())
	if err != nil {
		return fmt.Sprintf("Settings<redaction error: %v>", err)
	}
	return string(encoded)
}
func (s Settings) GoString() string { return s.String() }
