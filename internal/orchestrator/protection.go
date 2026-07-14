package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	venuepkg "github.com/wangchaozhi/cyp-agent/internal/venue"
)

// protectiveRemediationAttempts bounds re-placement so a persistently broken
// exchange cannot keep an unprotected position open indefinitely.
const protectiveRemediationAttempts = 2

type protectivePlacer interface {
	PlaceProtectiveOrders(
		ctx context.Context,
		clientID string,
		symbol string,
		side contracts.Side,
		marginMode contracts.MarginMode,
		stopLoss *contracts.Decimal,
		takeProfit *contracts.Decimal,
	) error
}

// remediateProtection restores missing native protection for a filled entry
// whose journal state is protective_failed. Every attempt re-places a
// standalone TP/SL algo and then re-verifies it against the exchange, because
// an acknowledgement alone is exactly the false positive this path exists to
// fix. When remediation is exhausted the position is deterministically
// flattened through the durable reduce-only path, safety freezes, and a
// critical alert fires.
func (s *Service) remediateProtection(
	ctx context.Context,
	runID string,
	intent contracts.OrderIntent,
	mark contracts.Decimal,
) error {
	var takeProfit *contracts.Decimal
	if len(intent.TakeProfit) > 0 && intent.TakeProfit[0].IsPositive() {
		price := intent.TakeProfit[0]
		takeProfit = &price
	}
	verifier, canVerify := s.venue.(protectiveOrderController)
	placer, canPlace := s.venue.(protectivePlacer)
	filledBase := contracts.Zero()
	if tracked, ok := s.journal.Get(intent.ClientID); ok && tracked.Result != nil {
		filledBase = tracked.Result.FilledBase
	}
	attemptErrors := make([]error, 0, protectiveRemediationAttempts)
	if canVerify && canPlace {
		for attempt := 1; attempt <= protectiveRemediationAttempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := placer.PlaceProtectiveOrders(
				ctx, intent.ClientID, intent.Symbol, intent.Side, intent.MarginMode, intent.StopLoss, takeProfit,
			); err != nil {
				attemptErrors = append(attemptErrors, fmt.Errorf("attempt %d place: %w", attempt, err))
				continue
			}
			verified, verifyErr := verifier.ProtectiveOrders(ctx, intent.Symbol)
			if verifyErr != nil {
				attemptErrors = append(attemptErrors, fmt.Errorf("attempt %d verify: %w", attempt, verifyErr))
				continue
			}
			if s.protectionSatisfied(intent, filledBase, verified) {
				if err := s.journal.TransitionContext(ctx, runID+":protective-remediated", intent.ClientID,
					contracts.OrderStatusProtectivePlaced, nil, "protective orders re-placed and verified"); err != nil {
					s.freezeDurability("protective remediation journal persistence failed")
					return err
				}
				s.events.Emit("protective_remediated", runID, map[string]any{
					"symbol": intent.Symbol, "client_id": intent.ClientID, "attempt": attempt, "orders": verified,
				})
				s.alert(ctx, "warning", "protective_remediated", map[string]any{
					"symbol": intent.Symbol, "client_id": intent.ClientID, "attempt": attempt,
				})
				return nil
			}
			attemptErrors = append(attemptErrors,
				fmt.Errorf("attempt %d: exchange did not report the requested protection", attempt))
		}
	} else {
		attemptErrors = append(attemptErrors, errors.New("venue cannot place or verify protective orders"))
	}

	joined := errors.Join(attemptErrors...)
	if s.safety != nil {
		s.safety.Freeze("protective remediation exhausted: " + intent.Symbol)
	}
	s.alert(ctx, "critical", "protective_remediation_exhausted", map[string]any{
		"symbol": intent.Symbol, "client_id": intent.ClientID, "error": joined.Error(),
	})
	if err := s.journal.TransitionContext(ctx, runID+":flattening", intent.ClientID,
		contracts.OrderStatusFlattening, nil, "protective remediation exhausted; emergency flatten"); err != nil {
		s.freezeDurability("flattening journal persistence failed")
		return errors.Join(joined, err)
	}
	if err := s.flattenPosition(ctx, runID, intent, mark); err != nil {
		s.alert(ctx, "critical", "emergency_flatten_failed", map[string]any{
			"symbol": intent.Symbol, "client_id": intent.ClientID, "error": err.Error(),
		})
		return errors.Join(joined, fmt.Errorf("emergency flatten: %w", err))
	}
	s.events.Emit("emergency_flattened", runID, map[string]any{
		"symbol": intent.Symbol, "client_id": intent.ClientID,
	})
	s.alert(ctx, "critical", "emergency_flattened", map[string]any{
		"symbol": intent.Symbol, "client_id": intent.ClientID,
	})
	return fmt.Errorf("protective remediation exhausted; position flattened: %w", joined)
}

// flattenPosition closes the unprotected position via the shared durable
// reduce-only path. CompleteReduceOnly finishes the lifecycle of the original
// order (flattening -> closed) through its source-order sweep.
func (s *Service) flattenPosition(
	ctx context.Context,
	runID string,
	intent contracts.OrderIntent,
	mark contracts.Decimal,
) error {
	positions, err := s.venue.Positions(ctx)
	if err != nil {
		return fmt.Errorf("load positions for emergency flatten: %w", err)
	}
	var target *contracts.Position
	for index := range positions {
		if positions[index].Symbol == intent.Symbol && positions[index].Instrument == intent.Instrument {
			position := positions[index]
			target = &position
			break
		}
	}
	if target == nil {
		if err := s.clearProtectiveOrders(ctx, intent.Symbol); err != nil {
			return err
		}
		if err := s.journal.TransitionContext(ctx, runID+":flatten-closed", intent.ClientID,
			contracts.OrderStatusClosed, nil, "position already flat before emergency flatten"); err != nil {
			s.freezeDurability("emergency flatten journal persistence failed")
			return err
		}
		return nil
	}
	if !mark.IsPositive() {
		if fetched, tickerErr := s.venue.FetchTicker(ctx, target.Symbol); tickerErr == nil && fetched.IsPositive() {
			mark = fetched
		} else {
			mark = target.EntryPrice
		}
	}
	marginMode := contracts.MarginModeIsolated
	if target.MarginMode != nil {
		marginMode = *target.MarginMode
	}
	clientID := "flatten-" + runID
	closeIntent := contracts.OrderIntent{
		ClientID: clientID, Symbol: target.Symbol, Venue: s.venue.ID(), Side: target.Side,
		Instrument: target.Instrument, OrderType: contracts.EntryTypeMarket,
		SizeQuote: target.SizeBase.Mul(mark), Price: &mark, Leverage: target.Leverage,
		MarginMode: marginMode, ReduceOnly: true, TakeProfit: contracts.List[contracts.Decimal]{},
	}
	execution, execErr := s.ExecuteReduceOnly(ctx, runID+":flatten", closeIntent)
	if execErr != nil && execution.Status != contracts.OrderStatusFilled {
		return execErr
	}
	if execution.Status != contracts.OrderStatusFilled {
		if execution.Error != nil && strings.TrimSpace(*execution.Error) != "" {
			return errors.New(*execution.Error)
		}
		return fmt.Errorf("emergency flatten order status is %s", execution.Status)
	}
	if err := s.FinalizeClose(ctx, *target); err != nil {
		return err
	}
	if err := s.CompleteReduceOnly(ctx, runID+":flatten", clientID, execution); err != nil {
		return err
	}
	if s.riskState != nil {
		balances, balanceErr := s.venue.Balances(ctx)
		if balanceErr != nil {
			return fmt.Errorf("post-flatten balances: %w", balanceErr)
		}
		equity := balances.TotalQuote
		if !equity.IsPositive() {
			equity = balances.FreeQuote
		}
		reference := runID
		if opened, ok := s.riskState.OpenTrade(target.Symbol, target.Instrument); ok && opened.RunID != "" {
			reference = opened.RunID
		}
		if _, stateErr := s.riskState.RecordClose(ctx, reference, *target, execution, equity); stateErr != nil {
			s.freezeDurability("emergency flatten risk state persistence failed")
			return fmt.Errorf("persist emergency flatten close: %w", stateErr)
		}
	}
	return execErr
}

// protectionSatisfied checks the exchange-reported orders against what the
// intent required. A verified reduce-only stop loss (and take profit, when
// requested) with a positive trigger price is mandatory.
func (s *Service) protectionSatisfied(
	intent contracts.OrderIntent,
	filledBase contracts.Decimal,
	orders []contracts.ProtectiveOrder,
) bool {
	needStop := intent.StopLoss != nil && intent.StopLoss.IsPositive()
	needTake := len(intent.TakeProfit) > 0 && intent.TakeProfit[0].IsPositive()
	strictOKX := s.venue != nil && s.venue.Kind() == venuepkg.KindCEX && s.venue.ID() == "okx"
	expectedClientID := venuepkg.SanitizeOKXClientID(intent.ClientID, "protect")
	hasStop, hasTake := false, false
	for _, order := range orders {
		if !order.TriggerPrice.IsPositive() || !order.ReduceOnly {
			continue
		}
		if strictOKX {
			if order.ClientID != expectedClientID || order.PositionSide != intent.Side {
				continue
			}
			if !order.FullClose && (!order.SizeBase.IsPositive() || order.SizeBase.Cmp(filledBase) < 0) {
				continue
			}
		}
		switch strings.ToLower(strings.TrimSpace(order.Kind)) {
		case "stop_loss":
			if !needStop || order.TriggerPrice.Cmp(*intent.StopLoss) == 0 {
				hasStop = true
			}
		case "take_profit":
			if !needTake || order.TriggerPrice.Cmp(intent.TakeProfit[0]) == 0 {
				hasTake = true
			}
		}
	}
	if needStop && !hasStop {
		return false
	}
	if needTake && !hasTake {
		return false
	}
	return true
}

func (s *Service) alert(ctx context.Context, level, message string, fields map[string]any) {
	if s.alerter == nil {
		return
	}
	_ = s.alerter.Alert(ctx, level, message, fields)
}
