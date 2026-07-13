// Package llm provides read-only model clients and resilience controls. It has
// no dependency on venue, approval, or execution packages and cannot place an
// order.
package llm

import (
	"context"
	"encoding/json"
	"time"
)

type TextRequest struct {
	System string
	User   string
	Model  string
}

type JSONRequest struct {
	System string
	User   string
	Model  string
	Schema json.RawMessage
}

type Usage struct {
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	CostUSD        float64 `json:"cost_usd"`
	TokenEstimated bool    `json:"token_estimated"`
	CostEstimated  bool    `json:"cost_estimated"`
}

func (usage Usage) TotalTokens() int { return usage.InputTokens + usage.OutputTokens }

type Completion struct {
	Text  string
	JSON  json.RawMessage
	Model string
	Usage Usage
}

type Provider interface {
	Name() string
	Text(context.Context, TextRequest) (Completion, error)
	JSON(context.Context, JSONRequest) (Completion, error)
}

type UsageMetadata struct {
	RunID  string
	Symbol string
	Agent  string
	Source string
}

type usageMetadataKey struct{}

func WithUsageMetadata(ctx context.Context, metadata UsageMetadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	current, _ := ctx.Value(usageMetadataKey{}).(UsageMetadata)
	if metadata.RunID != "" {
		current.RunID = metadata.RunID
	}
	if metadata.Symbol != "" {
		current.Symbol = metadata.Symbol
	}
	if metadata.Agent != "" {
		current.Agent = metadata.Agent
	}
	if metadata.Source != "" {
		current.Source = metadata.Source
	}
	return context.WithValue(ctx, usageMetadataKey{}, current)
}

func usageMetadata(ctx context.Context) UsageMetadata {
	if ctx == nil {
		return UsageMetadata{}
	}
	metadata, _ := ctx.Value(usageMetadataKey{}).(UsageMetadata)
	return metadata
}

type UsageEvent struct {
	ID             string    `json:"id"`
	TS             time.Time `json:"ts"`
	RunID          string    `json:"run_id,omitempty"`
	Symbol         string    `json:"symbol,omitempty"`
	Agent          string    `json:"agent,omitempty"`
	Source         string    `json:"source,omitempty"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	Operation      string    `json:"operation"`
	Status         string    `json:"status"`
	InputTokens    int       `json:"input_tokens"`
	OutputTokens   int       `json:"output_tokens"`
	CostUSD        float64   `json:"cost_usd"`
	DurationMS     int64     `json:"duration_ms"`
	TokenEstimated bool      `json:"token_estimated"`
	CostEstimated  bool      `json:"cost_estimated"`
	ErrorKind      string    `json:"error_kind,omitempty"`
}

func (event UsageEvent) TotalTokens() int { return event.InputTokens + event.OutputTokens }

type UsageObserver interface {
	Reserve(context.Context, UsageEvent) error
	Record(context.Context, UsageEvent)
}
