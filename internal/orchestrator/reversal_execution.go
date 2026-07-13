package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/approval"
	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/portfolio"
	"github.com/wangchaozhi/cyp-agent/internal/risk"
	"github.com/wangchaozhi/cyp-agent/internal/riskstate"
)

type positionRelation uint8

const (
	positionAbsent positionRelation = iota
	positionSameSide
	positionOppositeSide
)

type protectiveOrderController interface {
	ProtectiveOrders(context.Context, string) ([]contracts.ProtectiveOrder, error)
	CancelProtectiveOrders(context.Context, string) error
}

func positionForProposal(positions []contracts.Position, proposal contracts.TradeProposal) (*contracts.Position, positionRelation) {
	for _, position := range positions {
		if position.Symbol != proposal.Symbol || position.Instrument != proposal.Instrument {
			continue
		}
		copy := position
		if position.Side == proposal.Side {
			return &copy, positionSameSide
		}
		return &copy, positionOppositeSide
	}
	return nil, positionAbsent
}

func positionsWithout(positions []contracts.Position, excluded *contracts.Position) []contracts.Position {
	if excluded == nil {
		return positions
	}
	result := make([]contracts.Position, 0, len(positions))
	for _, position := range positions {
		if position.Symbol == excluded.Symbol && position.Instrument == excluded.Instrument {
			continue
		}
		result = append(result, position)
	}
	return result
}

func (s *Service) decideReversal(
	runID string,
	proposal contracts.TradeProposal,
	settings config.Settings,
	metrics AutoApprovalMetrics,
	position contracts.Position,
) approval.DecisionResult {
	reject := func(note string) approval.DecisionResult {
		decision := contracts.ApprovalDecision{
			Decision: contracts.ApprovalReject, Operator: "auto-reverse-policy",
			TS: time.Now().UTC(), Note: note,
		}
		return approval.DecisionResult{Decision: decision, FinalProposal: proposal}
	}
	if !metrics.Allowed {
		s.reversals.Reset(position.Symbol, position.Instrument)
		return reject("自动反向拒绝：" + metrics.Reason)
	}
	if proposal.Confidence < settings.Automation.ReverseMinConfidence {
		s.reversals.Reset(position.Symbol, position.Instrument)
		return reject("自动反向拒绝：置信度低于反向阈值")
	}
	if metrics.RewardRisk < settings.Automation.ReverseMinRewardRisk {
		s.reversals.Reset(position.Symbol, position.Instrument)
		return reject("自动反向拒绝：盈亏比低于反向阈值")
	}
	trades := []riskstate.TradeRecord{}
	if s.riskState != nil {
		trades = s.riskState.Trades()
	}
	reversal := s.reversals.Observe(position, proposal, time.Now().UTC(), settings.Automation, trades)
	s.events.Emit("reversal_observed", runID, map[string]any{
		"symbol": proposal.Symbol, "position_side": position.Side,
		"proposal_side": proposal.Side, "model": metrics, "reversal": reversal,
	})
	if !reversal.Ready {
		return reject(reversal.Reason)
	}
	proposal.SizeQuote = metrics.RecommendedQuote
	decision := contracts.ApprovalDecision{
		Decision: contracts.ApprovalApprove, Operator: "auto-reverse-policy",
		TS: time.Now().UTC(), Note: autoApprovalNote(metrics) + "；" + reversal.Reason,
	}
	return approval.DecisionResult{Decision: decision, FinalProposal: proposal}
}

func (s *Service) closeForReversal(
	ctx context.Context,
	runID string,
	position *contracts.Position,
	mark contracts.Decimal,
) error {
	if position == nil {
		return errors.New("opposite position is missing")
	}
	marginMode := contracts.MarginModeIsolated
	if position.MarginMode != nil {
		marginMode = *position.MarginMode
	}
	clientID := "reverse-close-" + runID
	intent := contracts.OrderIntent{
		ClientID: clientID, Symbol: position.Symbol, Venue: s.venue.ID(), Side: position.Side,
		Instrument: position.Instrument, OrderType: contracts.EntryTypeMarket,
		SizeQuote: position.SizeBase.Mul(mark), Price: &mark, Leverage: position.Leverage,
		MarginMode: marginMode, ReduceOnly: true, TakeProfit: contracts.List[contracts.Decimal]{},
	}
	if err := s.checkpoint(ctx, runID, "reversal_close_intent", intent); err != nil {
		return err
	}
	_ = s.journal.Open(runID+":reverse-close:open", intent)
	_ = s.journal.Transition(runID+":reverse-close:submit", clientID, contracts.OrderStatusSubmitting, nil, "")
	execution, err := s.venue.Place(ctx, intent)
	if err != nil {
		status := contracts.OrderStatusFailed
		if s.freezeUnknownOrder(err) {
			status = contracts.OrderStatusUnknown
		}
		_ = s.journal.Transition(runID+":reverse-close:fail", clientID, status, nil, err.Error())
		return err
	}
	s.recordExecution(runID+":reverse-close", clientID, execution)
	if execution.Status != contracts.OrderStatusFilled {
		if execution.Error != nil && strings.TrimSpace(*execution.Error) != "" {
			return errors.New(*execution.Error)
		}
		return fmt.Errorf("close order status is %s", execution.Status)
	}
	finalizeErr := s.FinalizeClose(ctx, *position)
	balances, balanceErr := s.venue.Balances(ctx)
	equity := balances.TotalQuote
	if !equity.IsPositive() {
		equity = balances.FreeQuote
	}
	reference := runID
	var pnl contracts.Decimal
	var stateErr error
	if balanceErr == nil && s.riskState != nil {
		if opened, ok := s.riskState.OpenTrade(position.Symbol, position.Instrument); ok && opened.RunID != "" {
			reference = opened.RunID
		}
		var record riskstate.TradeRecord
		record, stateErr = s.riskState.RecordClose(ctx, reference, *position, execution, equity)
		if stateErr == nil {
			pnl = record.PNLQuote
		}
	}
	var reviewErr error
	if balanceErr == nil && stateErr == nil {
		_, reviewErr = s.ReviewClosed(ctx, *position, execution, pnl, reference)
	}
	if joined := errors.Join(
		wrapReversalError("finalize close", finalizeErr),
		wrapReversalError("post-close balances", balanceErr),
		wrapReversalError("persist close", stateErr),
		wrapReversalError("review close", reviewErr),
	); joined != nil {
		return joined
	}
	s.events.Emit("reversal_closed", runID, map[string]any{
		"symbol": position.Symbol, "side": position.Side, "execution": execution, "pnl_quote": pnl,
	})
	return nil
}

func wrapReversalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func (s *Service) waitUntilFlat(ctx context.Context, symbol string, instrument contracts.Instrument) error {
	for attempt := 0; attempt < 4; attempt++ {
		positions, err := s.venue.Positions(ctx)
		if err != nil {
			return fmt.Errorf("verify closed position: %w", err)
		}
		found := false
		for _, position := range positions {
			if position.Symbol == symbol && position.Instrument == instrument {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
		if attempt < 3 {
			timer := time.NewTimer(150 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return errors.New("position still exists after reduce-only close")
}

// FinalizeClose verifies that a reduce-only fill actually flattened the
// position and removes residual TP/SL algorithms before the same symbol may be
// opened again. Manual, automated, and reversal closes share this invariant.
func (s *Service) FinalizeClose(ctx context.Context, position contracts.Position) error {
	if err := s.waitUntilFlat(ctx, position.Symbol, position.Instrument); err != nil {
		return err
	}
	return s.clearProtectiveOrders(ctx, position.Symbol)
}

func (s *Service) clearProtectiveOrders(ctx context.Context, symbol string) error {
	if !s.venue.Caps().NativeProtectiveOrders {
		return nil
	}
	controller, ok := s.venue.(protectiveOrderController)
	if !ok {
		return errors.New("venue cannot inspect and cancel residual protective orders")
	}
	if err := controller.CancelProtectiveOrders(ctx, symbol); err != nil {
		return fmt.Errorf("cancel residual protective orders: %w", err)
	}
	remaining, err := controller.ProtectiveOrders(ctx, symbol)
	if err != nil {
		return fmt.Errorf("verify residual protective orders: %w", err)
	}
	if len(remaining) != 0 {
		return fmt.Errorf("%d residual protective orders remain after close", len(remaining))
	}
	return nil
}

func (s *Service) refreshRiskAfterReversal(
	ctx context.Context,
	proposal contracts.TradeProposal,
	mark contracts.Decimal,
	settings config.Settings,
) (contracts.RiskAssessment, contracts.TradeProposal, error) {
	if !settings.Automation.Enabled || !settings.Automation.EntryEnabled ||
		!settings.Automation.ApprovalEnabled || !settings.Automation.ReverseEnabled {
		assessment := contracts.RiskAssessment{
			Verdict: contracts.VerdictRejected, RiskScore: 1,
			HardViolations: contracts.List[string]{"reverse reopen policy: automatic reversal was disabled after close"},
		}
		return assessment, proposal, nil
	}
	balances, err := s.venue.Balances(ctx)
	if err != nil {
		return contracts.RiskAssessment{}, proposal, err
	}
	equity := balances.TotalQuote
	if !equity.IsPositive() {
		equity = balances.FreeQuote
	}
	accountRisk := riskstate.Snapshot{CurrentEquity: equity}
	if s.riskState != nil {
		if err := s.riskState.ObserveEquity(ctx, equity); err != nil {
			return contracts.RiskAssessment{}, proposal, err
		}
		accountRisk = s.riskState.Snapshot(equity)
	}
	positions, err := s.venue.Positions(ctx)
	if err != nil {
		return contracts.RiskAssessment{}, proposal, err
	}
	if existing, relation := positionForProposal(positions, proposal); relation != positionAbsent || existing != nil {
		return contracts.RiskAssessment{}, proposal, errors.New("account is not flat before reverse reopen")
	}
	preflight, err := s.venue.Preflight(ctx, intentFor("reverse-recheck", proposal, proposal.SizeQuote))
	if err != nil {
		return contracts.RiskAssessment{}, proposal, err
	}
	if !preflight.OK {
		assessment := contracts.RiskAssessment{
			Verdict: contracts.VerdictRejected, RiskScore: 1,
			HardViolations: contracts.List[string]{"reverse preflight: " + strings.Join(preflight.Reasons, "; ")},
		}
		return assessment, proposal, nil
	}
	view := portfolio.Build(positions, s.Marks(), equity, settings.Risk.MaxCorrelatedExposure)
	correlated := portfolio.CorrelatedDirectional(view, proposal.Symbol, proposal.Side)
	reconciling := false
	if s.safety != nil {
		snapshot := s.safety.Snapshot()
		reconciling = snapshot.Frozen || snapshot.ReconcileActive
	}
	context := contracts.RiskContext{
		EquityQuote: equity, RefPrice: mark, GrossExposureQuote: view.Gross,
		SymbolExposureQuote:     portfolio.SymbolNotional(view, proposal.Symbol),
		CorrelatedExposureQuote: &correlated, PortfolioCVARQuote: accountRisk.PortfolioCVARQuote,
		OrdersLastHour: accountRisk.OrdersLastHour, ConsecutiveLosses: accountRisk.ConsecutiveLosses,
		DailyDrawdown: accountRisk.DailyDrawdown, WeeklyDrawdown: accountRisk.WeeklyDrawdown,
		TotalDrawdown: accountRisk.TotalDrawdown, Kill: settings.Kill, Reconciling: reconciling,
		EstimatedSlippageBPS:      preflight.EstSlippageBPS,
		EstimatedLiquidationPrice: preflight.EstLiquidationPrice,
		EstimatedPriceImpact:      preflight.EstPriceImpact,
	}
	assessment := risk.Assess(proposal, context, limitsFromConfig(settings.Risk))
	if assessment.Verdict == contracts.VerdictRejected {
		return assessment, proposal, nil
	}
	metrics := evaluateAutoApproval(settings, proposal, assessment, equity)
	if !metrics.Allowed {
		assessment.Verdict = contracts.VerdictRejected
		assessment.HardViolations = append(assessment.HardViolations, "reverse reopen policy: "+metrics.Reason)
		return assessment, proposal, nil
	}
	proposal.SizeQuote = metrics.RecommendedQuote
	if assessment.AdjustedSizeQuote != nil && assessment.AdjustedSizeQuote.Cmp(proposal.SizeQuote) < 0 {
		proposal.SizeQuote = *assessment.AdjustedSizeQuote
	}
	assessment, proposal, err = s.reassessExecutableProposal(
		ctx, "reverse-final-pf", proposal, context, settings,
	)
	if err != nil {
		return contracts.RiskAssessment{}, proposal, err
	}
	if assessment.RiskScore > settings.Automation.MaxRiskScore {
		assessment.Verdict = contracts.VerdictRejected
		assessment.HardViolations = append(assessment.HardViolations,
			fmt.Sprintf("reverse auto_risk_score: 最终风险分 %.2f > 自动审批上限 %.2f",
				assessment.RiskScore, settings.Automation.MaxRiskScore))
	}
	s.events.Emit("reversal_reassessed", "-", map[string]any{
		"symbol": proposal.Symbol, "assessment": assessment, "model": metrics,
	})
	return assessment, proposal, nil
}
