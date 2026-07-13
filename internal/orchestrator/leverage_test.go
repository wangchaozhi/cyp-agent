package orchestrator

import (
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func TestApplyLeverageModelReactsToFinalNotional(t *testing.T) {
	entry := contracts.MustDecimal("100")
	stop := contracts.MustDecimal("95")
	proposal := contracts.TradeProposal{
		Symbol: "BTC/USDT:USDT", Venue: "okx", Side: contracts.SideLong,
		Instrument: contracts.InstrumentPerp, SizeQuote: contracts.MustDecimal("2000"),
		Leverage: 1, MarginMode: contracts.MarginModeIsolated,
		Entry:    contracts.PricePlan{Type: contracts.EntryTypeMarket, Price: &entry},
		StopLoss: &stop, TakeProfit: contracts.List[contracts.Decimal]{contracts.MustDecimal("110")},
		Confidence: 0.8, SupportingReports: contracts.List[string]{},
	}
	if err := applyLeverageModel(&proposal, contracts.MustDecimal("10000"), config.DefaultRiskConfig()); err != nil {
		t.Fatal(err)
	}
	if proposal.Leverage != 2 || proposal.LeveragePlan == nil {
		t.Fatalf("initial leverage = %+v", proposal)
	}
	proposal.SizeQuote = contracts.MustDecimal("200")
	if err := applyLeverageModel(&proposal, contracts.MustDecimal("10000"), config.DefaultRiskConfig()); err != nil {
		t.Fatal(err)
	}
	if proposal.Leverage != 1 || proposal.LeveragePlan.EstimatedMarginQuote.String() != "200" {
		t.Fatalf("final leverage did not fall to 1x: %+v", proposal)
	}
}

func TestApplyLeverageModelUsesAggregateAddOnNotionalAndKeepsExistingLeverage(t *testing.T) {
	entry := contracts.MustDecimal("100")
	stop := contracts.MustDecimal("95")
	proposal := contracts.TradeProposal{
		Symbol: "BTC/USDT:USDT", Venue: "okx", Side: contracts.SideLong,
		Instrument: contracts.InstrumentPerp, SizeQuote: contracts.MustDecimal("200"),
		Leverage: 2, MarginMode: contracts.MarginModeIsolated,
		Entry: contracts.PricePlan{Type: contracts.EntryTypeMarket, Price: &entry}, StopLoss: &stop,
		TakeProfit: contracts.List[contracts.Decimal]{contracts.MustDecimal("110")},
		Confidence: 0.8, SupportingReports: contracts.List[string]{},
		AddOnPlan: &contracts.AddOnPlan{
			Model: addOnModelVersion, ExistingNotionalQuote: contracts.MustDecimal("1000"),
			ExistingLeverage: 2, ProfitR: 1, AddIndex: 1, MaxAdds: 2,
			RiskDecay: 0.5, RiskFraction: 0.005, MaxPositionFraction: 0.5,
			RecommendedNotionalQuote: contracts.MustDecimal("200"), CooldownMinutes: 60,
		},
	}
	if err := applyLeverageModel(&proposal, contracts.MustDecimal("10000"), config.DefaultRiskConfig()); err != nil {
		t.Fatal(err)
	}
	if proposal.Leverage != 2 || proposal.SizeQuote.Cmp(contracts.MustDecimal("200")) != 0 ||
		proposal.LeveragePlan == nil || proposal.LeveragePlan.EstimatedMarginQuote.Cmp(contracts.MustDecimal("600")) != 0 {
		t.Fatalf("aggregate leverage result: %+v", proposal)
	}
}
