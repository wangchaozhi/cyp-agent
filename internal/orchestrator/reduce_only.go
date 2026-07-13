package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

// ExecuteReduceOnly is the single durable submission path for manual,
// automated and reversal closes. A persistence failure before Place aborts;
// a failure after a venue fill returns both the fill and the error so callers
// can finish risk/protection cleanup without ever retrying the close blindly.
func (s *Service) ExecuteReduceOnly(
	ctx context.Context,
	eventPrefix string,
	intent contracts.OrderIntent,
) (contracts.ExecutionResult, error) {
	if ctx == nil {
		return contracts.ExecutionResult{}, errors.New("reduce-only context is required")
	}
	if !intent.ReduceOnly {
		return contracts.ExecutionResult{}, errors.New("close execution requires reduce_only=true")
	}
	if strings.TrimSpace(intent.ClientID) == "" {
		return contracts.ExecutionResult{}, errors.New("close execution requires client_id")
	}
	eventPrefix = strings.TrimSpace(eventPrefix)
	if eventPrefix == "" {
		eventPrefix = "close:" + intent.ClientID
	}
	if err := s.journal.OpenContext(ctx, eventPrefix+":open", intent); err != nil {
		s.freezeDurability("reduce-only intent journal persistence failed")
		return contracts.ExecutionResult{}, fmt.Errorf("persist reduce-only intent: %w", err)
	}
	if err := s.journal.TransitionContext(ctx, eventPrefix+":submit", intent.ClientID, contracts.OrderStatusSubmitting, nil, ""); err != nil {
		s.freezeDurability("reduce-only submission journal persistence failed")
		return contracts.ExecutionResult{}, fmt.Errorf("persist reduce-only submission: %w", err)
	}
	execution, err := s.venue.Place(ctx, intent)
	if err != nil {
		status := contracts.OrderStatusFailed
		if s.freezeUnknownOrder(err) {
			status = contracts.OrderStatusUnknown
		}
		journalErr := s.journal.TransitionContext(ctx, eventPrefix+":fail", intent.ClientID, status, nil, err.Error())
		if journalErr != nil {
			s.freezeDurability("reduce-only failure journal persistence failed")
		}
		return contracts.ExecutionResult{}, errors.Join(err, journalErr)
	}
	if err := s.recordExecution(ctx, eventPrefix, intent.ClientID, execution); err != nil {
		s.freezeDurability("reduce-only result journal persistence failed")
		return execution, fmt.Errorf("persist reduce-only execution result: %w", err)
	}
	return execution, nil
}

// CompleteReduceOnly closes the durable lifecycle only after the caller has
// verified the venue is flat and residual protective orders are gone.
func (s *Service) CompleteReduceOnly(ctx context.Context, eventPrefix, clientID string, result contracts.ExecutionResult) error {
	eventPrefix = strings.TrimSpace(eventPrefix)
	if eventPrefix == "" {
		eventPrefix = "close:" + clientID
	}
	if err := s.journal.TransitionContext(ctx, eventPrefix+":closed", clientID, contracts.OrderStatusClosed, &result, "position flat and protection cleared"); err != nil {
		s.freezeDurability("reduce-only completion journal persistence failed")
		return fmt.Errorf("persist reduce-only completion: %w", err)
	}
	closeOrder, _ := s.journal.Get(clientID)
	for _, source := range s.journal.Orders() {
		if source.ClientID == clientID || source.Intent.Symbol != closeOrder.Intent.Symbol ||
			source.Intent.Instrument != closeOrder.Intent.Instrument {
			continue
		}
		if source.Status != contracts.OrderStatusFilled && source.Status != contracts.OrderStatusProtectivePlaced &&
			source.Status != contracts.OrderStatusProtectiveFailed {
			continue
		}
		sourceEventID := eventPrefix + ":source:" + source.ClientID + ":closed"
		if err := s.journal.TransitionContext(ctx, sourceEventID, source.ClientID, contracts.OrderStatusClosed, nil, "position closed by reduce-only order "+clientID); err != nil {
			s.freezeDurability("source order completion journal persistence failed")
			return fmt.Errorf("persist source order %s completion: %w", source.ClientID, err)
		}
	}
	return nil
}
