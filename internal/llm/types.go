// Package llm provides read-only model clients and resilience controls. It has
// no dependency on venue, approval, or execution packages and cannot place an
// order.
package llm

import (
	"context"
	"encoding/json"
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
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
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
