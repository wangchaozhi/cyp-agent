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
	LLMProvider     *string `json:"llm_provider,omitempty"`
	LLMModel        *string `json:"llm_model,omitempty"`
	LLMModelFast    *string `json:"llm_model_fast,omitempty"`
	LLMBaseURL      *string `json:"llm_base_url,omitempty"`
	AnthropicAPIKey *string `json:"anthropic_api_key,omitempty"`
	DeepSeekAPIKey  *string `json:"deepseek_api_key,omitempty"`
}

type BacktestRequest struct {
	Symbol    *string `json:"symbol,omitempty"`
	Bars      int     `json:"bars"`
	Window    int     `json:"window"`
	Seed      int     `json:"seed"`
	Drift     float64 `json:"drift"`
	Vol       float64 `json:"vol"`
	Data      string  `json:"data"`
	Timeframe string  `json:"timeframe"`
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
