package risk

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

const LeverageModelVersion = "margin-volatility-v1"

// LeverageInput separates notional risk from financing. ProposedNotional is
// sized elsewhere (risk budget/Kelly); this model only finds the lowest
// leverage that fits the margin budget without violating stress distance.
type LeverageInput struct {
	EquityQuote              contracts.Decimal
	ProposedNotionalQuote    contracts.Decimal
	StopFraction             contracts.Decimal
	VolatilityFraction       contracts.Decimal
	MaxLeverage              contracts.Decimal
	MinLeverage              contracts.Decimal
	MaxMarginPct             contracts.Decimal
	LeverageStep             contracts.Decimal
	MinLiquidationBuffer     contracts.Decimal
	StopLossBufferMultiple   contracts.Decimal
	VolatilityBufferMultiple contracts.Decimal
	LiquidationReservePct    contracts.Decimal
}

type LeverageResult struct {
	NotionalQuote contracts.Decimal
	Plan          contracts.LeveragePlan
}

// CalculateLeverage implements:
//
//	M = equity * max_margin_pct
//	B = max(min_liq_buffer, stop_multiple*stop, vol_multiple*sigma) + reserve
//	L_safe = floor_step(min(max_leverage, 1/B))
//	N_final = min(N_proposed, M*L_safe)
//	L = ceil_step(max(1, N_final/M))
//
// Choosing the minimum feasible L preserves liquidation distance. If the
// requested notional needs more than L_safe, notional is reduced instead.
func CalculateLeverage(input LeverageInput) (LeverageResult, error) {
	one := contracts.NewDecimalFromInt64(1)
	minimumLeverage := input.MinLeverage
	if !minimumLeverage.IsPositive() {
		minimumLeverage = one
	}
	if !input.EquityQuote.IsPositive() || !input.ProposedNotionalQuote.IsPositive() {
		return LeverageResult{}, errors.New("equity and proposed notional must be positive")
	}
	if input.StopFraction.IsNegative() || input.VolatilityFraction.IsNegative() {
		return LeverageResult{}, errors.New("stop and volatility fractions cannot be negative")
	}
	if input.MaxLeverage.Cmp(one) < 0 || minimumLeverage.Cmp(one) < 0 || minimumLeverage.Cmp(input.MaxLeverage) > 0 ||
		!input.MaxMarginPct.IsPositive() || input.MaxMarginPct.Cmp(one) > 0 ||
		!input.LeverageStep.IsPositive() || input.LeverageStep.Cmp(one) > 0 {
		return LeverageResult{}, errors.New("invalid leverage or margin limits")
	}
	if !input.MinLiquidationBuffer.IsPositive() || !input.StopLossBufferMultiple.IsPositive() ||
		!input.VolatilityBufferMultiple.IsPositive() || input.LiquidationReservePct.IsNegative() {
		return LeverageResult{}, errors.New("invalid liquidation stress parameters")
	}

	stressBuffer := decimalMax(
		input.MinLiquidationBuffer,
		input.StopFraction.Mul(input.StopLossBufferMultiple),
		input.VolatilityFraction.Mul(input.VolatilityBufferMultiple),
	).Add(input.LiquidationReservePct)
	if stressBuffer.Cmp(one) >= 0 {
		return LeverageResult{}, fmt.Errorf("required liquidation buffer %s leaves no feasible leverage", stressBuffer)
	}

	bufferLeverage, err := one.Quo(stressBuffer)
	if err != nil {
		return LeverageResult{}, fmt.Errorf("calculate buffer leverage: %w", err)
	}
	safeRaw := decimalMin(input.MaxLeverage, bufferLeverage)
	safeRawFloat, err := safeRaw.Float64()
	if err != nil {
		return LeverageResult{}, fmt.Errorf("convert safe leverage: %w", err)
	}
	step, err := input.LeverageStep.Float64()
	if err != nil || step <= 0 {
		return LeverageResult{}, errors.New("invalid leverage step")
	}
	safeLeverage := floorLeverageStep(safeRawFloat, step)
	minimumFloat, err := minimumLeverage.Float64()
	if err != nil {
		return LeverageResult{}, fmt.Errorf("convert minimum leverage: %w", err)
	}
	if safeLeverage+1e-9 < minimumFloat {
		return LeverageResult{}, fmt.Errorf("safe leverage %.8g is below existing minimum %.8g", safeLeverage, minimumFloat)
	}
	safeDecimal, err := leverageDecimal(safeLeverage)
	if err != nil {
		return LeverageResult{}, err
	}

	marginBudget := input.EquityQuote.Mul(input.MaxMarginPct)
	maxNotional := marginBudget.Mul(safeDecimal)
	adjustedNotional := input.ProposedNotionalQuote
	downSized := adjustedNotional.Cmp(maxNotional) > 0
	if downSized {
		adjustedNotional = maxNotional
	}
	requiredRatio, err := input.ProposedNotionalQuote.Quo(marginBudget)
	if err != nil {
		return LeverageResult{}, fmt.Errorf("calculate required leverage: %w", err)
	}
	requiredRaw, err := requiredRatio.Float64()
	if err != nil {
		return LeverageResult{}, fmt.Errorf("convert required leverage: %w", err)
	}
	requiredLeverage := ceilLeverageStep(math.Max(1, requiredRaw), step)

	selectedRatio, err := adjustedNotional.Quo(marginBudget)
	if err != nil {
		return LeverageResult{}, fmt.Errorf("calculate selected leverage: %w", err)
	}
	selectedRaw, err := selectedRatio.Float64()
	if err != nil {
		return LeverageResult{}, fmt.Errorf("convert selected leverage: %w", err)
	}
	selectedLeverage := ceilLeverageStep(math.Max(minimumFloat, selectedRaw), step)
	if selectedLeverage > safeLeverage+1e-9 {
		return LeverageResult{}, fmt.Errorf("selected leverage %.8g exceeds safe maximum %.8g", selectedLeverage, safeLeverage)
	}
	selectedDecimal, err := leverageDecimal(selectedLeverage)
	if err != nil {
		return LeverageResult{}, err
	}
	estimatedMargin, err := adjustedNotional.Quo(selectedDecimal)
	if err != nil {
		return LeverageResult{}, fmt.Errorf("calculate estimated margin: %w", err)
	}

	return LeverageResult{
		NotionalQuote: adjustedNotional,
		Plan: contracts.LeveragePlan{
			Model: LeverageModelVersion, RequiredLeverage: requiredLeverage,
			SafeMaxLeverage: safeLeverage, SelectedLeverage: selectedLeverage,
			StopFraction: input.StopFraction, VolatilityFraction: input.VolatilityFraction,
			RequiredLiquidationBuffer: stressBuffer, MarginBudgetQuote: marginBudget,
			EstimatedMarginQuote: estimatedMargin, MaxNotionalQuote: maxNotional,
			Downsized: downSized,
		},
	}, nil
}

func decimalMax(values ...contracts.Decimal) contracts.Decimal {
	result := values[0]
	for _, value := range values[1:] {
		if value.Cmp(result) > 0 {
			result = value
		}
	}
	return result
}

func decimalMin(left, right contracts.Decimal) contracts.Decimal {
	if left.Cmp(right) < 0 {
		return left
	}
	return right
}

func floorLeverageStep(value, step float64) float64 {
	return math.Floor(value/step+1e-12) * step
}

func ceilLeverageStep(value, step float64) float64 {
	return math.Ceil(value/step-1e-12) * step
}

func leverageDecimal(value float64) (contracts.Decimal, error) {
	return contracts.ParseDecimal(strconv.FormatFloat(value, 'g', 15, 64))
}
