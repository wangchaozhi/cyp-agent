package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/risk"
)

func applyLeverageModel(proposal *contracts.TradeProposal, equity contracts.Decimal, limits config.RiskConfig) error {
	if proposal == nil {
		return errors.New("proposal is nil")
	}
	if !isOpeningProposal(*proposal) || proposal.Instrument != contracts.InstrumentPerp {
		proposal.Leverage = 1
		proposal.LeveragePlan = nil
		return proposal.Validate()
	}
	entry := proposalEntryPrice(proposal.Entry)
	if entry == nil || !entry.IsPositive() || proposal.StopLoss == nil {
		return errors.New("perpetual leverage model requires entry and stop prices")
	}
	stopFraction, err := entry.Sub(*proposal.StopLoss).Abs().Quo(*entry)
	if err != nil || !stopFraction.IsPositive() {
		return errors.New("perpetual leverage model requires a positive stop fraction")
	}
	volatilityFraction := contracts.Zero()
	if proposal.LeveragePlan != nil {
		volatilityFraction = proposal.LeveragePlan.VolatilityFraction
	}
	proposedNotional := proposal.SizeQuote
	minimumLeverage := contracts.NewDecimalFromInt64(1)
	existingNotional := contracts.Zero()
	if proposal.AddOnPlan != nil {
		existingNotional = proposal.AddOnPlan.ExistingNotionalQuote
		proposedNotional = proposedNotional.Add(existingNotional)
		minimumLeverage, err = contracts.ParseDecimal(fmt.Sprintf("%.17g", proposal.AddOnPlan.ExistingLeverage))
		if err != nil {
			return errors.New("existing add-on leverage is invalid")
		}
	}
	calculation, err := risk.CalculateLeverage(risk.LeverageInput{
		EquityQuote: equity, ProposedNotionalQuote: proposedNotional,
		StopFraction: stopFraction, VolatilityFraction: volatilityFraction,
		MaxLeverage: limits.MaxLeverage, MinLeverage: minimumLeverage,
		MaxMarginPct: limits.MaxMarginPct,
		LeverageStep: limits.LeverageStep, MinLiquidationBuffer: limits.MinLiqBuffer,
		StopLossBufferMultiple:   limits.LiqStopMultiple,
		VolatilityBufferMultiple: limits.LiqVolMultiple,
		LiquidationReservePct:    limits.LiqReservePct,
	})
	if err != nil {
		return err
	}
	adjustedOrderNotional := calculation.NotionalQuote.Sub(existingNotional)
	if !adjustedOrderNotional.IsPositive() {
		return errors.New("existing position consumes all safe leverage capacity")
	}
	proposal.SizeQuote = adjustedOrderNotional
	proposal.Leverage = calculation.Plan.SelectedLeverage
	plan := calculation.Plan
	proposal.LeveragePlan = &plan
	return proposal.Validate()
}

func isOpeningProposal(proposal contracts.TradeProposal) bool {
	return proposal.Side == contracts.SideLong || proposal.Side == contracts.SideShort
}

// reassessExecutableProposal recalculates leverage, refreshes venue preflight,
// and runs deterministic risk again after any automatic/operator size change.
// A risk downsize is fed back through the leverage model until stable.
func (s *Service) reassessExecutableProposal(
	ctx context.Context,
	clientID string,
	proposal contracts.TradeProposal,
	riskContext contracts.RiskContext,
	settings config.Settings,
) (contracts.RiskAssessment, contracts.TradeProposal, error) {
	downsizeReasons := contracts.List[string]{}
	for attempt := 0; attempt < 3; attempt++ {
		beforeModel := proposal.SizeQuote
		if err := applyLeverageModel(&proposal, riskContext.EquityQuote, settings.Risk); err != nil {
			return contracts.RiskAssessment{}, proposal, fmt.Errorf("leverage model: %w", err)
		}
		if proposal.SizeQuote.Cmp(beforeModel) < 0 {
			downsizeReasons = append(downsizeReasons, fmt.Sprintf(
				"leverage_model: 所需杠杆超过安全上限，仓位由 %s 缩至 %s", beforeModel, proposal.SizeQuote))
		}

		preflight, err := s.venue.Preflight(ctx, intentFor(clientID, proposal, proposal.SizeQuote))
		if err != nil {
			return contracts.RiskAssessment{}, proposal, fmt.Errorf("preflight: %w", err)
		}
		if !preflight.OK {
			return contracts.RiskAssessment{
				Verdict: contracts.VerdictRejected, RiskScore: 1,
				HardViolations: contracts.List[string]{"preflight: " + strings.Join(preflight.Reasons, "; ")},
			}, proposal, nil
		}
		riskContext.EstimatedSlippageBPS = preflight.EstSlippageBPS
		riskContext.EstimatedLiquidationPrice = preflight.EstLiquidationPrice
		riskContext.EstimatedPriceImpact = preflight.EstPriceImpact
		assessment := risk.Assess(proposal, riskContext, limitsFromConfig(settings.Risk))
		if assessment.Verdict == contracts.VerdictRejected {
			return assessment, proposal, nil
		}
		if assessment.AdjustedSizeQuote != nil && assessment.AdjustedSizeQuote.Cmp(proposal.SizeQuote) < 0 {
			downsizeReasons = append(downsizeReasons, assessment.HardViolations...)
			proposal.SizeQuote = *assessment.AdjustedSizeQuote
			continue
		}
		if len(downsizeReasons) > 0 {
			assessment.Verdict = contracts.VerdictDownsized
			assessment.HardViolations = downsizeReasons
		}
		return assessment, proposal, nil
	}
	return contracts.RiskAssessment{}, proposal, errors.New("risk and leverage downsizing did not converge")
}
