package config

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type LoadOptions struct {
	// EnvFile defaults to .env in Load. An empty value in LoadWithOptions
	// disables dotenv loading.
	EnvFile   string
	LookupEnv func(string) (string, bool)
}

func Load() (Settings, error) {
	return LoadWithOptions(LoadOptions{EnvFile: ".env", LookupEnv: os.LookupEnv})
}

func LoadFile(path string) (Settings, error) {
	return LoadWithOptions(LoadOptions{EnvFile: path, LookupEnv: os.LookupEnv})
}

func LoadWithOptions(options LoadOptions) (Settings, error) {
	settings := DefaultSettings()
	dotenv, err := readDotEnv(options.EnvFile)
	if err != nil {
		return Settings{}, err
	}
	lookupEnv := options.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	lookup := func(key string) (string, bool) {
		if value, ok := lookupEnv(key); ok {
			return value, true
		}
		value, ok := dotenv[strings.ToUpper(key)]
		return value, ok
	}

	setString := func(key string, destination *string) {
		if value, ok := lookup(key); ok {
			*destination = strings.TrimSpace(value)
		}
	}
	setSecret := func(key string, destination *Secret) {
		if value, ok := lookup(key); ok {
			*destination = Secret(strings.TrimSpace(value))
		}
	}
	setBool := func(key string, destination *bool) error {
		value, ok := lookup(key)
		if !ok {
			return nil
		}
		parsed, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		*destination = parsed
		return nil
	}
	setInt := func(key string, destination *int) error {
		value, ok := lookup(key)
		if !ok {
			return nil
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("%s must be an integer: %w", key, err)
		}
		*destination = parsed
		return nil
	}
	setFloat := func(key string, destination *float64) error {
		value, ok := lookup(key)
		if !ok {
			return nil
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return fmt.Errorf("%s must be a finite number", key)
		}
		*destination = parsed
		return nil
	}
	setDecimal := func(key string, destination *contracts.Decimal) error {
		value, ok := lookup(key)
		if !ok {
			return nil
		}
		parsed, err := contracts.ParseDecimal(value)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		*destination = parsed
		return nil
	}
	setOptionalDecimal := func(key string, destination **contracts.Decimal) error {
		value, ok := lookup(key)
		if !ok {
			return nil
		}
		if strings.TrimSpace(value) == "" {
			*destination = nil
			return nil
		}
		parsed, err := contracts.ParseDecimal(value)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		*destination = &parsed
		return nil
	}

	setString("CYP_MODE", &settings.Mode)
	setString("CYP_APPROVAL", &settings.Approval)
	// "cli" never had a terminal approver; it always blocked on the same
	// pending gate the dashboard resolves. Keep it as a deprecated alias so
	// existing .env files continue to work.
	if settings.Approval == "cli" {
		settings.Approval = "dashboard"
	}
	setString("CYP_AUTO_SYMBOLS", &settings.AutoSymbols)
	setString("CYP_EXECUTION_VENUE", &settings.ExecutionVenue)
	setString("CYP_DATA_SOURCE", &settings.DataSource)
	setString("CYP_LLM_PROVIDER", &settings.LLMProvider)
	setString("CYP_LLM_MODEL", &settings.LLMModel)
	setString("CYP_LLM_MODEL_FAST", &settings.LLMModelFast)
	setString("CYP_LLM_BASE_URL", &settings.LLMBaseURL)
	setSecret("ANTHROPIC_API_KEY", &settings.AnthropicAPIKey)
	setSecret("DEEPSEEK_API_KEY", &settings.DeepSeekAPIKey)
	setString("CYP_CEX_ID", &settings.CEXID)
	setSecret("BINANCE_API_KEY", &settings.BinanceAPIKey)
	setSecret("BINANCE_API_SECRET", &settings.BinanceAPISecret)
	setSecret("OKX_API_KEY", &settings.OKXAPIKey)
	setSecret("OKX_API_SECRET", &settings.OKXAPISecret)
	setSecret("OKX_PASSWORD", &settings.OKXPassword)
	setString("CYP_ALERT_WEBHOOK", &settings.AlertWebhook)
	setString("CYP_EVM_RPC_URL", &settings.EVMRPCURL)
	setString("CYP_SIGNER", &settings.Signer)
	setString("CYP_ONCHAIN_DATA_API", &settings.OnchainDataAPI)
	setString("CYP_WATCHLIST", &settings.Watchlist)
	setString("CYP_DB_URL", &settings.DBURL)
	setString("CYP_PERSISTENCE", &settings.Persistence)
	setString("CYP_STATE_FILE", &settings.StateFile)
	setString("CYP_LOG_LEVEL", &settings.LogLevel)
	setSecret("CYP_API_TOKEN", &settings.APIToken)
	setString("CYP_CONTRACT_WHITELIST", &settings.Risk.ContractWhitelist)
	_, automationMasterExplicit := lookup("CYP_AUTOMATION_ENABLED")

	for _, operation := range []func() error{
		func() error { return setFloat("CYP_AUTO_MAX_RISK_SCORE", &settings.Automation.MaxRiskScore) },
		func() error { return setDecimal("CYP_AUTO_MAX_QUOTE", &settings.Automation.MaxQuote) },
		func() error { return setBool("CYP_AUTOMATION_ENABLED", &settings.Automation.Enabled) },
		func() error { return setBool("CYP_AUTO_SCAN", &settings.Automation.ScanEnabled) },
		func() error { return setBool("CYP_AUTO_ENTRY", &settings.Automation.EntryEnabled) },
		func() error { return setBool("CYP_AUTO_APPROVE", &settings.Automation.ApprovalEnabled) },
		func() error { return setBool("CYP_AUTO_EXIT", &settings.Automation.ExitEnabled) },
		func() error { return setBool("CYP_AUTO_REVERSE", &settings.Automation.ReverseEnabled) },
		func() error { return setBool("CYP_AUTO_ADD", &settings.Automation.AddEnabled) },
		func() error { return setFloat("CYP_AUTO_MIN_CONFIDENCE", &settings.Automation.MinConfidence) },
		func() error { return setFloat("CYP_AUTO_MIN_REWARD_RISK", &settings.Automation.MinRewardRisk) },
		func() error { return setDecimal("CYP_AUTO_MIN_ENTRY_QUOTE", &settings.Automation.MinEntryQuote) },
		func() error { return setFloat("CYP_AUTO_KELLY_SCALE", &settings.Automation.KellyScale) },
		func() error { return setFloat("CYP_ADD_MIN_CONFIDENCE", &settings.Automation.AddMinConfidence) },
		func() error { return setFloat("CYP_ADD_MIN_PROFIT_R", &settings.Automation.AddMinProfitR) },
		func() error { return setFloat("CYP_ADD_RISK_DECAY", &settings.Automation.AddRiskDecay) },
		func() error {
			return setFloat("CYP_ADD_MAX_POSITION_FRACTION", &settings.Automation.AddMaxPositionFraction)
		},
		func() error { return setInt("CYP_ADD_COOLDOWN_MINUTES", &settings.Automation.AddCooldownMinutes) },
		func() error { return setInt("CYP_MAX_ADDS_PER_POSITION", &settings.Automation.MaxAddsPerPosition) },
		func() error { return setFloat("CYP_REVERSE_MIN_CONFIDENCE", &settings.Automation.ReverseMinConfidence) },
		func() error {
			return setFloat("CYP_REVERSE_MIN_REWARD_RISK", &settings.Automation.ReverseMinRewardRisk)
		},
		func() error { return setInt("CYP_REVERSE_CONFIRMATIONS", &settings.Automation.ReverseConfirmations) },
		func() error { return setInt("CYP_REVERSE_SIGNAL_MINUTES", &settings.Automation.ReverseSignalMinutes) },
		func() error { return setInt("CYP_REVERSE_COOLDOWN_MINUTES", &settings.Automation.ReverseCooldownMins) },
		func() error { return setInt("CYP_MAX_REVERSALS_PER_DAY", &settings.Automation.MaxReversalsPerDay) },
		func() error { return setFloat("CYP_EXIT_EWMA_LAMBDA", &settings.Automation.EWMALambda) },
		func() error {
			return setFloat("CYP_EXIT_VOLATILITY_MULTIPLIER", &settings.Automation.VolatilityMultiplier)
		},
		func() error { return setFloat("CYP_EXIT_TRAIL_ACTIVATION_R", &settings.Automation.TrailActivationR) },
		func() error { return setFloat("CYP_EXIT_TRAIL_GIVEBACK_R", &settings.Automation.TrailGivebackR) },
		func() error { return setFloat("CYP_EXIT_PROFIT_TARGET_R", &settings.Automation.ProfitTargetR) },
		func() error { return setFloat("CYP_EXIT_LOSS_CUT_R", &settings.Automation.LossCutR) },
		func() error { return setInt("CYP_EXIT_MAX_HOLDING_MINUTES", &settings.Automation.MaxHoldingMinutes) },
		func() error { return setFloat("CYP_EXIT_TIME_STOP_MIN_R", &settings.Automation.TimeStopMinR) },
		func() error { return setInt("CYP_EXIT_CONFIRMATIONS", &settings.Automation.ExitConfirmations) },
		func() error { return setInt("CYP_EXIT_MIN_SAMPLES", &settings.Automation.ExitMinSamples) },
		func() error { return setBool("CYP_KILL", &settings.Kill) },
		func() error { return setBool("CYP_ALLOW_PERP", &settings.AllowPerp) },
		func() error { return setBool("CYP_LIVE_ACK", &settings.LiveAck) },
		func() error { return setBool("CYP_OKX_DEMO", &settings.OKXDemo) },
		func() error { return setBool("CYP_RUNTIME_AUTOSTART", &settings.RuntimeAutostart) },
		func() error { return setInt("CYP_SCAN_INTERVAL", &settings.ScanInterval) },
		func() error { return setInt("CYP_MONITOR_INTERVAL", &settings.MonitorInterval) },
		func() error { return setInt("CYP_MAX_CONCURRENCY", &settings.MaxConcurrency) },
		func() error { return setBool("CYP_OHLCV_ARCHIVE_ENABLED", &settings.OHLCVArchiveEnabled) },
		func() error { return setInt("CYP_OHLCV_RETENTION_DAYS", &settings.OHLCVRetentionDays) },
		func() error { return setBool("CYP_TOKEN_USAGE_ENABLED", &settings.TokenUsageEnabled) },
		func() error { return setInt("CYP_TOKEN_USAGE_RETENTION_DAYS", &settings.TokenUsageRetentionDays) },
		func() error { setString("CYP_TOKEN_USAGE_TIMEZONE", &settings.TokenUsageTimezone); return nil },
		func() error { return setDecimal("CYP_MAX_RISK_PER_TRADE", &settings.Risk.MaxRiskPerTrade) },
		func() error { return setDecimal("CYP_MAX_POSITION_PCT", &settings.Risk.MaxPositionPct) },
		func() error { return setDecimal("CYP_MAX_GROSS_EXPOSURE", &settings.Risk.MaxGrossExposure) },
		func() error { return setDecimal("CYP_MAX_SYMBOL_CONCENTRATION", &settings.Risk.MaxSymbolConcentration) },
		func() error { return setDecimal("CYP_MAX_CORRELATED_EXPOSURE", &settings.Risk.MaxCorrelatedExposure) },
		func() error { return setDecimal("CYP_MAX_CVAR_PCT", &settings.Risk.MaxCVARPct) },
		func() error { return setInt("CYP_MAX_ORDERS_PER_HOUR", &settings.Risk.MaxOrdersPerHour) },
		func() error { return setDecimal("CYP_MAX_SLIPPAGE_BPS", &settings.Risk.MaxSlippageBPS) },
		func() error { return setDecimal("CYP_MAX_LEVERAGE", &settings.Risk.MaxLeverage) },
		func() error { return setDecimal("CYP_MAX_MARGIN_PCT", &settings.Risk.MaxMarginPct) },
		func() error { return setDecimal("CYP_LEVERAGE_STEP", &settings.Risk.LeverageStep) },
		func() error { return setDecimal("CYP_MIN_LIQ_BUFFER", &settings.Risk.MinLiqBuffer) },
		func() error { return setDecimal("CYP_LIQ_STOP_MULTIPLE", &settings.Risk.LiqStopMultiple) },
		func() error { return setDecimal("CYP_LIQ_VOL_MULTIPLE", &settings.Risk.LiqVolMultiple) },
		func() error { return setDecimal("CYP_LIQ_RESERVE_PCT", &settings.Risk.LiqReservePct) },
		func() error { return setBool("CYP_FORCE_ISOLATED", &settings.Risk.ForceIsolated) },
		func() error { return setDecimal("CYP_MIN_MARGIN_RATIO", &settings.Risk.MinMarginRatio) },
		func() error { return setDecimal("CYP_MAX_PRICE_IMPACT", &settings.Risk.MaxPriceImpact) },
		func() error { return setOptionalDecimal("CYP_MAX_GAS_GWEI", &settings.Risk.MaxGasGwei) },
		func() error { return setDecimal("CYP_MAX_GAS_QUOTE", &settings.Risk.MaxGasQuote) },
		func() error { return setDecimal("CYP_MIN_POOL_TVL", &settings.Risk.MinPoolTVL) },
		func() error { return setBool("CYP_REQUIRE_PRIVATE_MEMPOOL", &settings.Risk.RequirePrivateMempool) },
		func() error { return setDecimal("CYP_DAILY_DRAWDOWN_LIMIT", &settings.Risk.DailyDrawdownLimit) },
		func() error { return setDecimal("CYP_WEEKLY_DRAWDOWN_LIMIT", &settings.Risk.WeeklyDrawdownLimit) },
		func() error { return setDecimal("CYP_MAX_DRAWDOWN_LIMIT", &settings.Risk.MaxDrawdownLimit) },
		func() error { return setInt("CYP_MAX_CONSECUTIVE_LOSSES", &settings.Risk.MaxConsecutiveLosses) },
		func() error { return setInt("CYP_APPROVAL_TIMEOUT_SECONDS", &settings.Risk.ApprovalTimeoutSeconds) },
		func() error { return setInt("CYP_MAX_ITERATIONS", &settings.Budget.MaxIterations) },
		func() error { return setInt("CYP_MAX_TOKENS", &settings.Budget.MaxTokens) },
		func() error { return setFloat("CYP_MAX_COST_USD", &settings.Budget.MaxCostUSD) },
		func() error { return setInt("CYP_MAX_WALL_SECONDS", &settings.Budget.MaxWallSeconds) },
		func() error { return setInt("CYP_DAILY_TOKEN_BUDGET", &settings.Budget.DailyTokenBudget) },
		func() error { return setFloat("CYP_DAILY_COST_BUDGET_USD", &settings.Budget.DailyCostBudgetUSD) },
	} {
		if err := operation(); err != nil {
			return Settings{}, err
		}
	}
	// Preserve legacy CYP_APPROVAL=auto installations once, while allowing a
	// persisted/dashboard master switch to turn automation off definitively.
	if settings.Approval == "auto" && !automationMasterExplicit {
		settings.Automation.Enabled = true
		settings.Automation.EntryEnabled = true
		settings.Automation.ApprovalEnabled = true
	}

	if err := settings.Validate(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func parseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "t", "yes", "y", "on":
		return true, nil
	case "0", "false", "f", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not a boolean", value)
	}
}

func (s Settings) Validate() error {
	if !oneOf(s.Mode, "paper", "live") {
		return fmt.Errorf("CYP_MODE must be paper or live, got %q", s.Mode)
	}
	if !oneOf(s.Approval, "cli", "dashboard", "auto") {
		return fmt.Errorf("CYP_APPROVAL must be cli, dashboard, or auto, got %q", s.Approval)
	}
	if !oneOf(s.ExecutionVenue, "paper", "binance", "okx") {
		return fmt.Errorf("CYP_EXECUTION_VENUE must be paper, binance, or okx, got %q", s.ExecutionVenue)
	}
	if !oneOf(s.DataSource, "synthetic", "cex") {
		return fmt.Errorf("CYP_DATA_SOURCE must be synthetic or cex, got %q", s.DataSource)
	}
	if !oneOf(s.LLMProvider, "anthropic", "deepseek") {
		return fmt.Errorf("CYP_LLM_PROVIDER must be anthropic or deepseek, got %q", s.LLMProvider)
	}
	if !oneOf(s.Signer, "keystore", "kms", "hardware") {
		return fmt.Errorf("CYP_SIGNER must be keystore, kms, or hardware, got %q", s.Signer)
	}
	if !oneOf(s.Persistence, "memory", "file", "postgres") {
		return fmt.Errorf("CYP_PERSISTENCE must be memory, file, or postgres, got %q", s.Persistence)
	}
	if s.Persistence == "file" && strings.TrimSpace(s.StateFile) == "" {
		return errors.New("CYP_STATE_FILE is required when CYP_PERSISTENCE=file")
	}
	if s.Persistence == "postgres" && strings.TrimSpace(s.DBURL) == "" {
		return errors.New("CYP_DB_URL is required when CYP_PERSISTENCE=postgres")
	}
	if s.OHLCVArchiveEnabled && strings.TrimSpace(s.DBURL) == "" {
		return errors.New("CYP_DB_URL is required when CYP_OHLCV_ARCHIVE_ENABLED=true")
	}
	if s.OHLCVRetentionDays < 30 || s.OHLCVRetentionDays > 3650 {
		return errors.New("CYP_OHLCV_RETENTION_DAYS must be between 30 and 3650")
	}
	if s.TokenUsageEnabled && strings.TrimSpace(s.DBURL) == "" {
		return errors.New("CYP_DB_URL is required when CYP_TOKEN_USAGE_ENABLED=true")
	}
	if s.TokenUsageRetentionDays < 1 || s.TokenUsageRetentionDays > 3650 {
		return errors.New("CYP_TOKEN_USAGE_RETENTION_DAYS must be between 1 and 3650")
	}
	if _, err := time.LoadLocation(strings.TrimSpace(s.TokenUsageTimezone)); err != nil {
		return fmt.Errorf("CYP_TOKEN_USAGE_TIMEZONE is invalid: %w", err)
	}
	if api := strings.TrimSpace(s.OnchainDataAPI); api != "" &&
		!strings.HasPrefix(api, "http://") && !strings.HasPrefix(api, "https://") {
		return errors.New("CYP_ONCHAIN_DATA_API must be an http(s) URL")
	}
	automation := s.Automation
	if math.IsNaN(automation.MaxRiskScore) || math.IsInf(automation.MaxRiskScore, 0) || automation.MaxRiskScore < 0 || automation.MaxRiskScore > 1 {
		return errors.New("CYP_AUTO_MAX_RISK_SCORE must be between 0 and 1")
	}
	if automation.MaxQuote.IsNegative() || automation.MinEntryQuote.IsNegative() {
		return errors.New("automatic entry quote limits cannot be negative")
	}
	if automation.MaxQuote.IsPositive() && automation.MinEntryQuote.Cmp(automation.MaxQuote) > 0 {
		return errors.New("CYP_AUTO_MIN_ENTRY_QUOTE cannot exceed CYP_AUTO_MAX_QUOTE")
	}
	for name, value := range map[string]float64{
		"CYP_AUTO_MIN_CONFIDENCE":        automation.MinConfidence,
		"CYP_AUTO_MIN_REWARD_RISK":       automation.MinRewardRisk,
		"CYP_AUTO_KELLY_SCALE":           automation.KellyScale,
		"CYP_ADD_MIN_CONFIDENCE":         automation.AddMinConfidence,
		"CYP_ADD_MIN_PROFIT_R":           automation.AddMinProfitR,
		"CYP_ADD_RISK_DECAY":             automation.AddRiskDecay,
		"CYP_ADD_MAX_POSITION_FRACTION":  automation.AddMaxPositionFraction,
		"CYP_REVERSE_MIN_CONFIDENCE":     automation.ReverseMinConfidence,
		"CYP_REVERSE_MIN_REWARD_RISK":    automation.ReverseMinRewardRisk,
		"CYP_EXIT_EWMA_LAMBDA":           automation.EWMALambda,
		"CYP_EXIT_VOLATILITY_MULTIPLIER": automation.VolatilityMultiplier,
		"CYP_EXIT_TRAIL_ACTIVATION_R":    automation.TrailActivationR,
		"CYP_EXIT_TRAIL_GIVEBACK_R":      automation.TrailGivebackR,
		"CYP_EXIT_PROFIT_TARGET_R":       automation.ProfitTargetR,
		"CYP_EXIT_LOSS_CUT_R":            automation.LossCutR,
		"CYP_EXIT_TIME_STOP_MIN_R":       automation.TimeStopMinR,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%s must be finite", name)
		}
	}
	if automation.MinConfidence < 0 || automation.MinConfidence > 1 || automation.MinRewardRisk <= 0 ||
		automation.KellyScale <= 0 || automation.KellyScale > 1 ||
		automation.AddMinConfidence < 0 || automation.AddMinConfidence > 1 ||
		automation.AddMinProfitR <= 0 || automation.AddRiskDecay <= 0 || automation.AddRiskDecay > 1 ||
		automation.AddMaxPositionFraction <= 0 || automation.AddMaxPositionFraction > 1 ||
		automation.AddCooldownMinutes < 0 || automation.MaxAddsPerPosition <= 0 ||
		automation.ReverseMinConfidence < 0 || automation.ReverseMinConfidence > 1 ||
		automation.ReverseMinRewardRisk <= 0 || automation.ReverseConfirmations <= 0 ||
		automation.ReverseSignalMinutes <= 0 || automation.ReverseCooldownMins < 0 ||
		automation.MaxReversalsPerDay <= 0 ||
		automation.EWMALambda <= 0 || automation.EWMALambda >= 1 || automation.VolatilityMultiplier < 0 ||
		automation.TrailActivationR <= 0 || automation.TrailGivebackR <= 0 ||
		automation.ProfitTargetR <= 0 || automation.LossCutR <= 0 || automation.MaxHoldingMinutes <= 0 ||
		automation.ExitConfirmations <= 0 || automation.ExitMinSamples < 2 {
		return errors.New("automation model parameters are outside their safe ranges")
	}
	if s.Mode == "live" && automation.Enabled {
		return errors.New("strategy automation cannot be enabled in live read-only mode")
	}
	if len(s.WatchlistSymbols()) == 0 {
		return errors.New("CYP_WATCHLIST must contain at least one symbol")
	}
	if s.ScanInterval <= 0 || s.MonitorInterval <= 0 || s.MaxConcurrency <= 0 {
		return errors.New("runtime intervals and CYP_MAX_CONCURRENCY must be positive")
	}
	if s.Budget.MaxIterations <= 0 || s.Budget.MaxTokens <= 0 || s.Budget.MaxWallSeconds <= 0 || s.Budget.MaxCostUSD < 0 {
		return errors.New("budget limits must be positive (cost may be zero)")
	}
	if s.Budget.DailyTokenBudget <= 0 {
		return errors.New("CYP_DAILY_TOKEN_BUDGET must be positive")
	}
	if s.Budget.DailyCostBudgetUSD <= 0 {
		return errors.New("CYP_DAILY_COST_BUDGET_USD must be positive")
	}
	if s.Risk.MaxOrdersPerHour <= 0 || s.Risk.MaxConsecutiveLosses <= 0 || s.Risk.ApprovalTimeoutSeconds <= 0 {
		return errors.New("risk count and timeout limits must be positive")
	}
	for name, value := range map[string]contracts.Decimal{
		"CYP_MAX_RISK_PER_TRADE":       s.Risk.MaxRiskPerTrade,
		"CYP_MAX_POSITION_PCT":         s.Risk.MaxPositionPct,
		"CYP_MAX_GROSS_EXPOSURE":       s.Risk.MaxGrossExposure,
		"CYP_MAX_SYMBOL_CONCENTRATION": s.Risk.MaxSymbolConcentration,
		"CYP_MAX_CORRELATED_EXPOSURE":  s.Risk.MaxCorrelatedExposure,
		"CYP_MAX_CVAR_PCT":             s.Risk.MaxCVARPct,
		"CYP_MAX_SLIPPAGE_BPS":         s.Risk.MaxSlippageBPS,
		"CYP_MAX_LEVERAGE":             s.Risk.MaxLeverage,
		"CYP_MAX_MARGIN_PCT":           s.Risk.MaxMarginPct,
		"CYP_LEVERAGE_STEP":            s.Risk.LeverageStep,
		"CYP_MIN_LIQ_BUFFER":           s.Risk.MinLiqBuffer,
		"CYP_LIQ_STOP_MULTIPLE":        s.Risk.LiqStopMultiple,
		"CYP_LIQ_VOL_MULTIPLE":         s.Risk.LiqVolMultiple,
		"CYP_LIQ_RESERVE_PCT":          s.Risk.LiqReservePct,
		"CYP_MIN_MARGIN_RATIO":         s.Risk.MinMarginRatio,
		"CYP_MAX_PRICE_IMPACT":         s.Risk.MaxPriceImpact,
		"CYP_MAX_GAS_QUOTE":            s.Risk.MaxGasQuote,
		"CYP_MIN_POOL_TVL":             s.Risk.MinPoolTVL,
		"CYP_DAILY_DRAWDOWN_LIMIT":     s.Risk.DailyDrawdownLimit,
		"CYP_WEEKLY_DRAWDOWN_LIMIT":    s.Risk.WeeklyDrawdownLimit,
		"CYP_MAX_DRAWDOWN_LIMIT":       s.Risk.MaxDrawdownLimit,
	} {
		if value.IsNegative() {
			return fmt.Errorf("%s cannot be negative", name)
		}
	}
	if s.Risk.MaxGasGwei != nil && s.Risk.MaxGasGwei.IsNegative() {
		return errors.New("CYP_MAX_GAS_GWEI cannot be negative")
	}
	if !s.Risk.MaxLeverage.IsPositive() || s.Risk.MaxLeverage.Cmp(contracts.NewDecimalFromInt64(1)) < 0 {
		return errors.New("CYP_MAX_LEVERAGE must be at least 1")
	}
	if !s.Risk.MaxMarginPct.IsPositive() || s.Risk.MaxMarginPct.Cmp(contracts.NewDecimalFromInt64(1)) > 0 {
		return errors.New("CYP_MAX_MARGIN_PCT must be greater than 0 and at most 1")
	}
	if !s.Risk.LeverageStep.IsPositive() || s.Risk.LeverageStep.Cmp(contracts.NewDecimalFromInt64(1)) > 0 {
		return errors.New("CYP_LEVERAGE_STEP must be positive and no greater than 1")
	}
	if !s.Risk.MinLiqBuffer.IsPositive() || !s.Risk.LiqStopMultiple.IsPositive() ||
		!s.Risk.LiqVolMultiple.IsPositive() || s.Risk.LiqReservePct.IsNegative() {
		return errors.New("liquidation model buffers and multipliers are outside their safe ranges")
	}
	if s.Risk.MinLiqBuffer.Add(s.Risk.LiqReservePct).Cmp(contracts.NewDecimalFromInt64(1)) >= 0 {
		return errors.New("CYP_MIN_LIQ_BUFFER plus CYP_LIQ_RESERVE_PCT must be below 1")
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func readDotEnv(path string) (map[string]string, error) {
	values := make(map[string]string)
	if strings.TrimSpace(path) == "" {
		return values, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open dotenv %q: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		index := strings.IndexByte(line, '=')
		if index <= 0 {
			return nil, fmt.Errorf("dotenv %s:%d: expected KEY=VALUE", path, lineNumber)
		}
		key := strings.TrimSpace(line[:index])
		if !validEnvKey(key) {
			return nil, fmt.Errorf("dotenv %s:%d: invalid key", path, lineNumber)
		}
		value, err := parseDotEnvValue(strings.TrimSpace(line[index+1:]))
		if err != nil {
			return nil, fmt.Errorf("dotenv %s:%d: %w", path, lineNumber, err)
		}
		values[strings.ToUpper(key)] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read dotenv %q: %w", path, err)
	}
	return values, nil
}

func validEnvKey(key string) bool {
	for index, r := range key {
		if index == 0 && !(r == '_' || unicode.IsLetter(r)) {
			return false
		}
		if index > 0 && !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return false
		}
	}
	return key != ""
}

func parseDotEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] == '\'' {
		end := strings.LastIndex(value[1:], "'")
		if end < 0 {
			return "", errors.New("unterminated single-quoted value")
		}
		end++
		if trailing := strings.TrimSpace(value[end+1:]); trailing != "" && !strings.HasPrefix(trailing, "#") {
			return "", errors.New("unexpected text after quoted value")
		}
		return value[1:end], nil
	}
	if value[0] == '"' {
		for end := 1; end < len(value); end++ {
			if value[end] != '"' || escapedAt(value, end) {
				continue
			}
			trailing := strings.TrimSpace(value[end+1:])
			if trailing != "" && !strings.HasPrefix(trailing, "#") {
				return "", errors.New("unexpected text after quoted value")
			}
			decoded, err := strconv.Unquote(value[:end+1])
			if err != nil {
				return "", fmt.Errorf("invalid double-quoted value: %w", err)
			}
			return decoded, nil
		}
		return "", errors.New("unterminated double-quoted value")
	}
	if index := strings.Index(value, " #"); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value), nil
}

func escapedAt(value string, index int) bool {
	backslashes := 0
	for index--; index >= 0 && value[index] == '\\'; index-- {
		backslashes++
	}
	return backslashes%2 == 1
}
