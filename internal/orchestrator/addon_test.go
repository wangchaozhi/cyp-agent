package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/riskstate"
)

func addOnFixture() (config.Settings, contracts.TradeProposal, contracts.Position, time.Time) {
	settings := config.DefaultSettings()
	settings.Automation.AddCooldownMinutes = 60
	entry := contracts.MustDecimal("110")
	stop := contracts.MustDecimal("105")
	proposal := contracts.TradeProposal{
		Symbol: "BTC/USDT:USDT", Venue: "okx", Side: contracts.SideLong,
		Instrument: contracts.InstrumentPerp, SizeQuote: contracts.MustDecimal("2000"),
		Leverage: 2, MarginMode: contracts.MarginModeIsolated,
		Entry: contracts.PricePlan{Type: contracts.EntryTypeMarket, Price: &entry}, StopLoss: &stop,
		TakeProfit: contracts.List[contracts.Decimal]{contracts.MustDecimal("120")},
		Confidence: 0.8, SupportingReports: contracts.List[string]{},
	}
	position := contracts.Position{
		Symbol: proposal.Symbol, Venue: "okx", Side: contracts.SideLong,
		Instrument: contracts.InstrumentPerp, SizeBase: contracts.MustDecimal("10"),
		EntryPrice: contracts.MustDecimal("100"), Leverage: 2,
	}
	return settings, proposal, position, time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
}

func TestEvaluateAddOnUsesDecayingRiskAndExistingPositionCap(t *testing.T) {
	settings, proposal, position, now := addOnFixture()
	result, evaluation := evaluateAddOn(settings, proposal, position,
		contracts.MustDecimal("110"), contracts.MustDecimal("10000"), nil, now)
	if !evaluation.Allowed || result.AddOnPlan == nil {
		t.Fatalf("add-on rejected: %+v", evaluation)
	}
	plan := result.AddOnPlan
	if plan.AddIndex != 1 || plan.ProfitR != 2 || plan.RiskFraction != 0.005 ||
		result.SizeQuote.Cmp(contracts.MustDecimal("550")) != 0 {
		t.Fatalf("unexpected add-on plan: %+v size=%s", plan, result.SizeQuote)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("add-on proposal invalid: %v", err)
	}
}

func TestEvaluateAddOnEnforcesTrendCooldownAndMaximumCount(t *testing.T) {
	settings, proposal, position, now := addOnFixture()
	proposal.Confidence = 0.7
	_, evaluation := evaluateAddOn(settings, proposal, position,
		contracts.MustDecimal("110"), contracts.MustDecimal("10000"), nil, now)
	if evaluation.Allowed || !strings.Contains(evaluation.Reason, "置信度") {
		t.Fatalf("weak signal was allowed: %+v", evaluation)
	}

	proposal.Confidence = 0.8
	trade := riskstate.TradeRecord{
		Kind: "open", Symbol: position.Symbol, Instrument: position.Instrument, Side: position.Side,
		SizeBase: position.SizeBase, TS: now.Add(-30 * time.Minute),
	}
	_, evaluation = evaluateAddOn(settings, proposal, position,
		contracts.MustDecimal("110"), contracts.MustDecimal("10000"), []riskstate.TradeRecord{trade}, now)
	if evaluation.Allowed || !strings.Contains(evaluation.Reason, "冷却") {
		t.Fatalf("cooldown was bypassed: %+v", evaluation)
	}

	settings.Automation.AddCooldownMinutes = 0
	position.SizeBase = contracts.MustDecimal("13")
	trades := []riskstate.TradeRecord{
		{Kind: "open", Symbol: position.Symbol, Instrument: position.Instrument, Side: position.Side, SizeBase: contracts.MustDecimal("10"), TS: now.Add(-3 * time.Hour)},
		{Kind: "open", Symbol: position.Symbol, Instrument: position.Instrument, Side: position.Side, SizeBase: contracts.MustDecimal("2"), TS: now.Add(-2 * time.Hour)},
		{Kind: "open", Symbol: position.Symbol, Instrument: position.Instrument, Side: position.Side, SizeBase: contracts.MustDecimal("1"), TS: now.Add(-time.Hour)},
	}
	_, evaluation = evaluateAddOn(settings, proposal, position,
		contracts.MustDecimal("110"), contracts.MustDecimal("10000"), trades, now)
	if evaluation.Allowed || !strings.Contains(evaluation.Reason, "最多") {
		t.Fatalf("maximum add count was bypassed: %+v", evaluation)
	}
}
