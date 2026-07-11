package portfolio

import (
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func TestBuildUsesMarksAndAlwaysReturnsBothClusters(t *testing.T) {
	positions := []contracts.Position{
		{Symbol: "BTC/USDT", Side: contracts.SideLong, Instrument: contracts.InstrumentSpot,
			SizeBase: contracts.MustDecimal("0.1"), EntryPrice: contracts.MustDecimal("50000"), Leverage: 1},
		{Symbol: "SOL/USDT", Side: contracts.SideShort, Instrument: contracts.InstrumentPerp,
			SizeBase: contracts.MustDecimal("2"), EntryPrice: contracts.MustDecimal("100"), Leverage: 2},
	}
	snapshot := Build(positions, map[string]contracts.Decimal{
		"BTC/USDT": contracts.MustDecimal("60000"),
	}, contracts.MustDecimal("10000"), contracts.MustDecimal("0.5"))
	if got := snapshot.Gross.String(); got != "6200.0" {
		t.Fatalf("gross = %s, want 6200.0", got)
	}
	if snapshot.Clusters["major"].Long.String() != "6000.0" || snapshot.Clusters["alt"].Short.String() != "200" {
		t.Fatalf("unexpected clusters: %#v", snapshot.Clusters)
	}
	if snapshot.CorrelatedLimit.String() != "5000.0" || snapshot.BySymbol == nil {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}
