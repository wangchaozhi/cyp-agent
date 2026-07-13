// Package tokenusage provides provider-neutral LLM usage attribution,
// persistence, reporting, and daily budget enforcement. Prompt and response
// content never enter this package.
package tokenusage

import (
	"context"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/llm"
)

type Summary struct {
	Day              string  `json:"day"`
	Timezone         string  `json:"timezone"`
	Calls            int     `json:"calls"`
	Successes        int     `json:"successes"`
	Errors           int     `json:"errors"`
	BudgetRejections int     `json:"budget_rejections"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	SuccessRate      float64 `json:"success_rate"`
	TokenBudget      int     `json:"token_budget"`
	CostBudgetUSD    float64 `json:"cost_budget_usd"`
	TokenRatio       float64 `json:"token_ratio"`
	CostRatio        float64 `json:"cost_ratio"`
	Utilization      float64 `json:"utilization"`
	Level            string  `json:"level"`
	Paused           bool    `json:"paused"`
}

type TrendBucket struct {
	Start        time.Time `json:"start"`
	Calls        int       `json:"calls"`
	Successes    int       `json:"successes"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	TotalTokens  int       `json:"total_tokens"`
	CostUSD      float64   `json:"cost_usd"`
}

type Dimension struct {
	Key          string  `json:"key"`
	Calls        int     `json:"calls"`
	Successes    int     `json:"successes"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type Report struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Days        int              `json:"days"`
	Bucket      string           `json:"bucket"`
	Today       Summary          `json:"today"`
	Trend       []TrendBucket    `json:"trend"`
	ByProvider  []Dimension      `json:"by_provider"`
	ByModel     []Dimension      `json:"by_model"`
	ByAgent     []Dimension      `json:"by_agent"`
	BySymbol    []Dimension      `json:"by_symbol"`
	BySource    []Dimension      `json:"by_source"`
	Recent      []llm.UsageEvent `json:"recent"`
}

func EmptyReport() Report {
	return Report{
		Trend: []TrendBucket{}, ByProvider: []Dimension{}, ByModel: []Dimension{},
		ByAgent: []Dimension{}, BySymbol: []Dimension{}, BySource: []Dimension{},
		Recent: []llm.UsageEvent{},
	}
}

type BudgetAlert struct {
	Level   string  `json:"level"`
	Ratio   float64 `json:"ratio"`
	Summary Summary `json:"summary"`
}

type Store interface {
	Load(context.Context, time.Time) ([]llm.UsageEvent, error)
	Save(context.Context, llm.UsageEvent, string, string) error
	Prune(context.Context, time.Time) (int64, error)
	Close()
}
