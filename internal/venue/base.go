// Package venue contains execution venue abstractions and the deterministic
// in-memory paper venue used by development, tests, and safe fallback paths.
package venue

import (
	"context"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

// Kind identifies the broad execution backend category.
type Kind string

// VenueKind is retained as an explicit alias for public interfaces and docs.
type VenueKind = Kind

const (
	KindPaper   Kind = "paper"
	KindCEX     Kind = "cex"
	KindOnchain Kind = "onchain"
)

// Caps describes features exposed by a venue. The JSON names match the
// existing /api/venues response.
type Caps struct {
	Spot                   bool `json:"spot"`
	Perp                   bool `json:"perp"`
	NativeProtectiveOrders bool `json:"native_protective_orders"`
	ReadOnly               bool `json:"read_only"`
}

// VenueCaps is retained as an explicit alias for public interfaces and docs.
type VenueCaps = Caps

// PreflightReport is a deterministic estimate consumed by the risk engine
// before an order may be placed.
type PreflightReport struct {
	OK                  bool                   `json:"ok"`
	EstPrice            contracts.Decimal      `json:"est_price"`
	EstSlippageBPS      *contracts.Decimal     `json:"est_slippage_bps,omitempty"`
	EstLiquidationPrice *contracts.Decimal     `json:"est_liq_price,omitempty"`
	EstPriceImpact      *contracts.Decimal     `json:"est_price_impact,omitempty"`
	Reasons             contracts.List[string] `json:"reasons"`
}

// Venue is the common surface used by orchestration, portfolio, and API code.
type Venue interface {
	ID() string
	Kind() Kind
	Caps() Caps
	IsConfigured() bool

	FetchTicker(context.Context, string) (contracts.Decimal, error)
	FetchOHLCV(context.Context, string, string, int) ([]contracts.Candle, error)
	FetchOrderBook(context.Context, string, int) (contracts.OrderBook, error)

	Positions(context.Context) ([]contracts.Position, error)
	Balances(context.Context) (contracts.Balances, error)
	Preflight(context.Context, contracts.OrderIntent) (PreflightReport, error)
	Place(context.Context, contracts.OrderIntent) (contracts.ExecutionResult, error)
	Cancel(context.Context, string) error
}
