package llm

import (
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/config"
)

// ConservativeCostEstimator is a safety ceiling, not a billing calculator.
// It intentionally overestimates many models so the configured dollar budget
// remains useful even when a provider omits cost metadata.
func ConservativeCostEstimator(_ string, usage Usage) float64 {
	const inputPerMillion = 15.0
	const outputPerMillion = 75.0
	return float64(usage.InputTokens)/1_000_000*inputPerMillion +
		float64(usage.OutputTokens)/1_000_000*outputPerMillion
}

func FromSettings(settings config.Settings) *Client {
	return FromSettingsWithObserver(settings, nil)
}

func FromSettingsWithObserver(settings config.Settings, observer UsageObserver) *Client {
	var provider Provider
	enabled := settings.LLMEnabled()
	if enabled && settings.LLMProvider == "deepseek" {
		baseURL := strings.TrimSpace(settings.LLMBaseURL)
		created, err := NewDeepSeekProvider(
			settings.DeepSeekAPIKey.Reveal(), baseURL, settings.LLMModel, nil,
		)
		if err == nil {
			provider = created
		} else {
			enabled = false
		}
	} else if enabled {
		created, err := NewAnthropicProvider(AnthropicConfig{
			APIKey: settings.AnthropicAPIKey.Reveal(), DefaultModel: settings.LLMModel,
		})
		if err == nil {
			provider = created
		} else {
			enabled = false
		}
	}
	if provider == nil {
		provider = NewMockProvider(0, true)
	}
	return NewClient(provider, Options{
		Enabled: enabled, Model: settings.LLMModel, FastModel: settings.LLMModelFast,
		MaxRetries: 2, BaseDelay: 200 * time.Millisecond, Timeout: 30 * time.Second,
		BreakerThreshold: 4, BreakerCooldown: 15 * time.Second,
		Budget: Budget{
			MaxCalls: settings.Budget.MaxIterations, MaxTokens: settings.Budget.MaxTokens,
			MaxCostUSD:  settings.Budget.MaxCostUSD,
			MaxWallTime: time.Duration(settings.Budget.MaxWallSeconds) * time.Second,
		},
		CostEstimator:   ConservativeCostEstimator,
		UsageObserver:   observer,
		MaxOutputTokens: 2048,
	})
}
