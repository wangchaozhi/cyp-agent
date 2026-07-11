// Package portfolio computes read-only aggregate exposure views from venue
// positions. It never mutates venue state and uses exact Decimal arithmetic.
package portfolio

import (
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type Directional struct {
	Long  contracts.Decimal `json:"long"`
	Short contracts.Decimal `json:"short"`
}

type SymbolExposure struct {
	Symbol  string            `json:"symbol"`
	Cluster string            `json:"cluster"`
	Long    contracts.Decimal `json:"long"`
	Short   contracts.Decimal `json:"short"`
}

type Snapshot struct {
	Equity          contracts.Decimal      `json:"equity"`
	NPositions      int                    `json:"n_positions"`
	Gross           contracts.Decimal      `json:"gross"`
	Clusters        map[string]Directional `json:"clusters"`
	BySymbol        []SymbolExposure       `json:"by_symbol"`
	CorrelatedLimit contracts.Decimal      `json:"correlated_limit"`
}

// Build returns the dashboard snapshot. A missing mark falls back to the entry
// price, using the same approximation across cross-symbol risk views.
func Build(
	positions []contracts.Position,
	marks map[string]contracts.Decimal,
	equity contracts.Decimal,
	maxCorrelatedExposure contracts.Decimal,
) Snapshot {
	clusters := map[string]Directional{
		"major": {Long: contracts.Zero(), Short: contracts.Zero()},
		"alt":   {Long: contracts.Zero(), Short: contracts.Zero()},
	}
	bySymbol := make(map[string]SymbolExposure)
	gross := contracts.Zero()
	for _, position := range positions {
		price, ok := marks[position.Symbol]
		if !ok || !price.IsPositive() {
			price = position.EntryPrice
		}
		notional := position.NotionalAt(price)
		gross = gross.Add(notional)
		cluster := ClusterOf(position.Symbol)
		directional := clusters[cluster]
		exposure := bySymbol[position.Symbol]
		if exposure.Symbol == "" {
			exposure = SymbolExposure{
				Symbol: position.Symbol, Cluster: cluster,
				Long: contracts.Zero(), Short: contracts.Zero(),
			}
		}
		if position.Side == contracts.SideShort {
			directional.Short = directional.Short.Add(notional)
			exposure.Short = exposure.Short.Add(notional)
		} else {
			directional.Long = directional.Long.Add(notional)
			exposure.Long = exposure.Long.Add(notional)
		}
		clusters[cluster] = directional
		bySymbol[position.Symbol] = exposure
	}

	ordered := make([]SymbolExposure, 0, len(bySymbol))
	seen := make(map[string]struct{}, len(bySymbol))
	for _, position := range positions {
		if _, ok := seen[position.Symbol]; ok {
			continue
		}
		seen[position.Symbol] = struct{}{}
		ordered = append(ordered, bySymbol[position.Symbol])
	}
	return Snapshot{
		Equity: equity, NPositions: len(positions), Gross: gross,
		Clusters: clusters, BySymbol: ordered,
		CorrelatedLimit: equity.Mul(maxCorrelatedExposure),
	}
}

// ClusterOf intentionally stays small and deterministic until a persisted
// correlation model is migrated. It matches the current dashboard split.
func ClusterOf(symbol string) string {
	base := strings.ToUpper(strings.TrimSpace(strings.SplitN(symbol, "/", 2)[0]))
	if base == "BTC" || base == "ETH" {
		return "major"
	}
	return "alt"
}

func Gross(snapshot Snapshot) contracts.Decimal { return snapshot.Gross }

func SymbolNotional(snapshot Snapshot, symbol string) contracts.Decimal {
	for _, exposure := range snapshot.BySymbol {
		if exposure.Symbol == symbol {
			return exposure.Long.Add(exposure.Short)
		}
	}
	return contracts.Zero()
}

func CorrelatedDirectional(snapshot Snapshot, symbol string, side contracts.Side) contracts.Decimal {
	value := snapshot.Clusters[ClusterOf(symbol)]
	if side == contracts.SideShort {
		return value.Short
	}
	return value.Long
}
