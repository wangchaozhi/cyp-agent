package contracts

type RunRequest struct {
	Symbol *string `json:"symbol,omitempty"`
}

type ApprovalRequest struct {
	Decision ApprovalAction `json:"decision"`
	Size     *Decimal       `json:"size,omitempty"`
	Note     string         `json:"note"`
	Operator string         `json:"operator"`
}

type KillRequest struct {
	On bool `json:"on"`
}

type ClosePositionRequest struct {
	Symbol     string     `json:"symbol"`
	Instrument Instrument `json:"instrument"`
}

type SettingsUpdateRequest struct {
	Mode            *string                   `json:"mode,omitempty"`
	Watchlist       *[]string                 `json:"watchlist,omitempty"`
	LLMProvider     *string                   `json:"llm_provider,omitempty"`
	LLMModel        *string                   `json:"llm_model,omitempty"`
	LLMModelFast    *string                   `json:"llm_model_fast,omitempty"`
	LLMBaseURL      *string                   `json:"llm_base_url,omitempty"`
	AnthropicAPIKey *string                   `json:"anthropic_api_key,omitempty"`
	DeepSeekAPIKey  *string                   `json:"deepseek_api_key,omitempty"`
	Automation      *AutomationSettingsUpdate `json:"automation,omitempty"`
}

type AutomationSettingsUpdate struct {
	Enabled              *bool    `json:"enabled,omitempty"`
	ScanEnabled          *bool    `json:"scan_enabled,omitempty"`
	ApprovalEnabled      *bool    `json:"approval_enabled,omitempty"`
	ExitEnabled          *bool    `json:"exit_enabled,omitempty"`
	MaxRiskScore         *float64 `json:"max_risk_score,omitempty"`
	MaxQuote             *Decimal `json:"max_quote,omitempty"`
	MinConfidence        *float64 `json:"min_confidence,omitempty"`
	MinRewardRisk        *float64 `json:"min_reward_risk,omitempty"`
	EWMALambda           *float64 `json:"ewma_lambda,omitempty"`
	VolatilityMultiplier *float64 `json:"volatility_multiplier,omitempty"`
	TrailActivationR     *float64 `json:"trail_activation_r,omitempty"`
	TrailGivebackR       *float64 `json:"trail_giveback_r,omitempty"`
	MaxHoldingMinutes    *int     `json:"max_holding_minutes,omitempty"`
	TimeStopMinR         *float64 `json:"time_stop_min_r,omitempty"`
	ExitConfirmations    *int     `json:"exit_confirmations,omitempty"`
	ExitMinSamples       *int     `json:"exit_min_samples,omitempty"`
}

type BacktestRequest struct {
	Symbol      *string `json:"symbol,omitempty"`
	Bars        int     `json:"bars"`
	Window      int     `json:"window"`
	Seed        int     `json:"seed"`
	Drift       float64 `json:"drift"`
	Vol         float64 `json:"vol"`
	Data        string  `json:"data"`
	Timeframe   string  `json:"timeframe"`
	FeeRate     float64 `json:"fee_rate"`
	SlippageBPS float64 `json:"slippage_bps"`
	SpreadBPS   float64 `json:"spread_bps"`
	FundingRate float64 `json:"funding_rate"`
}

type HealthStatus struct {
	OK             bool   `json:"ok"`
	Mode           string `json:"mode"`
	DisplayMode    string `json:"display_mode,omitempty"`
	ExecutionVenue string `json:"execution_venue,omitempty"`
	LLM            bool   `json:"llm"`
	Kill           bool   `json:"kill"`
}

type VenueInfo struct {
	ID                     string `json:"id"`
	Kind                   string `json:"kind"`
	Configured             bool   `json:"configured"`
	Spot                   bool   `json:"spot"`
	Perp                   bool   `json:"perp"`
	NativeProtectiveOrders bool   `json:"native_protective_orders"`
	ReadOnly               bool   `json:"read_only"`
}

type RunAccepted struct {
	RunID  string `json:"run_id"`
	Symbol string `json:"symbol"`
}

type OKResponse struct {
	OK bool `json:"ok"`
}

type KillStatus struct {
	Kill bool `json:"kill"`
}
