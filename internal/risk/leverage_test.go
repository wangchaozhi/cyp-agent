package risk

import (
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func defaultLeverageInput(notional string) LeverageInput {
	return LeverageInput{
		EquityQuote: contracts.MustDecimal("10000"), ProposedNotionalQuote: contracts.MustDecimal(notional),
		StopFraction: contracts.MustDecimal("0.05"), VolatilityFraction: contracts.MustDecimal("0.02"),
		MaxLeverage: contracts.MustDecimal("3"), MaxMarginPct: contracts.MustDecimal("0.10"),
		LeverageStep: contracts.MustDecimal("1"), MinLiquidationBuffer: contracts.MustDecimal("0.30"),
		StopLossBufferMultiple:   contracts.MustDecimal("2"),
		VolatilityBufferMultiple: contracts.MustDecimal("3"),
		LiquidationReservePct:    contracts.MustDecimal("0.02"),
	}
}

func TestCalculateLeverageSelectsLowestFeasibleLevel(t *testing.T) {
	result, err := CalculateLeverage(defaultLeverageInput("2000"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NotionalQuote.String() != "2000" || result.Plan.SelectedLeverage != 2 ||
		result.Plan.RequiredLeverage != 2 || result.Plan.SafeMaxLeverage != 3 || result.Plan.Downsized {
		t.Fatalf("unexpected leverage result: %+v", result)
	}
	if result.Plan.RequiredLiquidationBuffer.String() != "0.32" ||
		result.Plan.MarginBudgetQuote.Cmp(contracts.MustDecimal("1000")) != 0 ||
		result.Plan.EstimatedMarginQuote.Cmp(contracts.MustDecimal("1000")) != 0 ||
		result.Plan.MaxNotionalQuote.Cmp(contracts.MustDecimal("3000")) != 0 {
		t.Fatalf("unexpected leverage quantities: %+v", result.Plan)
	}

	result, err = CalculateLeverage(defaultLeverageInput("200"))
	if err != nil || result.Plan.SelectedLeverage != 1 || result.Plan.EstimatedMarginQuote.String() != "200" {
		t.Fatalf("small position should use 1x: %+v, %v", result, err)
	}
}

func TestCalculateLeverageDownsizesInsteadOfExceedingSafety(t *testing.T) {
	result, err := CalculateLeverage(defaultLeverageInput("5000"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NotionalQuote.Cmp(contracts.MustDecimal("3000")) != 0 || result.Plan.RequiredLeverage != 5 ||
		result.Plan.SelectedLeverage != 3 || !result.Plan.Downsized {
		t.Fatalf("unexpected downsize result: %+v", result)
	}

	input := defaultLeverageInput("2000")
	input.VolatilityFraction = contracts.MustDecimal("0.20")
	result, err = CalculateLeverage(input)
	if err != nil || result.Plan.SafeMaxLeverage != 1 || result.Plan.SelectedLeverage != 1 ||
		result.NotionalQuote.Cmp(contracts.MustDecimal("1000")) != 0 || result.Plan.RequiredLiquidationBuffer.String() != "0.62" {
		t.Fatalf("volatility stress was not applied: %+v, %v", result, err)
	}
}

func TestCalculateLeverageSupportsExchangeStepAndRejectsNoSolution(t *testing.T) {
	input := defaultLeverageInput("1250")
	input.MaxLeverage = contracts.MustDecimal("5")
	input.LeverageStep = contracts.MustDecimal("0.5")
	input.MinLiquidationBuffer = contracts.MustDecimal("0.18")
	result, err := CalculateLeverage(input)
	if err != nil || result.Plan.SelectedLeverage != 1.5 || result.Plan.SafeMaxLeverage != 5 {
		t.Fatalf("step result: %+v, %v", result, err)
	}

	input = defaultLeverageInput("1000")
	input.StopFraction = contracts.MustDecimal("0.50")
	if _, err := CalculateLeverage(input); err == nil {
		t.Fatal("stress buffer >= 100% unexpectedly produced leverage")
	}
}
