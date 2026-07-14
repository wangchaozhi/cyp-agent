package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/events"
	"github.com/wangchaozhi/cyp-agent/internal/observability"
)

type AutomatedExitVenue interface {
	ReconcileVenue
	FetchTicker(context.Context, string) (contracts.Decimal, error)
	Positions(context.Context) ([]contracts.Position, error)
}

type AutomatedExitFunc func(context.Context, contracts.Position, contracts.Decimal, ExitDecision) error

type AutomatedExitConfig struct {
	Venue      AutomatedExitVenue
	Interval   time.Duration
	Automation func() config.AutomationConfig
	State      RuntimeStateProvider
	OpenedAt   func(contracts.Position) (time.Time, bool)
	Exit       AutomatedExitFunc
	Events     *events.Bus
	Logger     *slog.Logger
	Model      *ExitModel
}

type AutomatedExitManager struct {
	venue      AutomatedExitVenue
	interval   time.Duration
	automation func() config.AutomationConfig
	state      RuntimeStateProvider
	openedAt   func(contracts.Position) (time.Time, bool)
	exit       AutomatedExitFunc
	events     *events.Bus
	logger     *slog.Logger
	model      *ExitModel
	wasEnabled bool
}

func NewAutomatedExitManager(settings AutomatedExitConfig) (*AutomatedExitManager, error) {
	if settings.Venue == nil || settings.Automation == nil || settings.Exit == nil {
		return nil, errors.New("automated exit venue, settings, and executor are required")
	}
	if settings.Interval <= 0 {
		return nil, errors.New("automated exit interval must be positive")
	}
	if settings.Logger == nil {
		settings.Logger = observability.DefaultLogger("automated-exit")
	}
	if settings.Model == nil {
		settings.Model = NewExitModel()
	}
	return &AutomatedExitManager{
		venue: settings.Venue, interval: settings.Interval, automation: settings.Automation,
		state: settings.State, openedAt: settings.OpenedAt, exit: settings.Exit, events: settings.Events,
		logger: settings.Logger, model: settings.Model,
	}, nil
}

func (manager *AutomatedExitManager) CheckOnce(ctx context.Context) error {
	if ctx == nil {
		return errors.New("automated exit context is required")
	}
	settings := manager.automation()
	enabled := settings.Enabled && settings.ExitEnabled
	if !enabled {
		if manager.wasEnabled {
			manager.model.Reset()
		}
		manager.wasEnabled = false
		return nil
	}
	manager.wasEnabled = true
	if err := ValidateExecutionVenue(manager.venue); err != nil {
		return err
	}
	if manager.state != nil {
		runtimeState := manager.state()
		policy, policyErr := ResolveModePolicy(runtimeState.Mode)
		if policyErr != nil {
			return policyErr
		}
		if executionErr := policy.ValidateExecution(runtimeState.executionTarget()); executionErr != nil {
			return executionErr
		}
	}
	positions, err := manager.venue.Positions(ctx)
	if err != nil {
		return fmt.Errorf("load positions for automated exit: %w", err)
	}
	active := make(map[string]struct{}, len(positions))
	errorsSeen := make([]error, 0)
	for _, position := range positions {
		active[exitPositionKey(position)] = struct{}{}
		mark, markErr := manager.venue.FetchTicker(ctx, position.Symbol)
		if markErr != nil {
			errorsSeen = append(errorsSeen, fmt.Errorf("%s automated exit ticker: %w", position.Symbol, markErr))
			continue
		}
		if !mark.IsPositive() {
			errorsSeen = append(errorsSeen, fmt.Errorf("%s automated exit ticker is not positive", position.Symbol))
			continue
		}
		orders, inspectable, inspectErr := inspectProtectiveOrders(ctx, manager.venue, position.Symbol)
		if inspectErr != nil {
			errorsSeen = append(errorsSeen, fmt.Errorf("%s automated exit protection: %w", position.Symbol, inspectErr))
			continue
		}
		stop, found := stopLossPrice(orders, position)
		if !inspectable || !found {
			errorsSeen = append(errorsSeen, fmt.Errorf("%s automated exit requires a verified stop loss", position.Symbol))
			continue
		}
		openedAt := time.Time{}
		if manager.openedAt != nil {
			openedAt, _ = manager.openedAt(position)
		}
		decision := manager.model.Observe(ExitObservation{
			Position: position, Mark: mark, StopLoss: stop, OpenedAt: openedAt, Now: time.Now().UTC(),
		}, settings)
		if manager.events != nil && decision.Reason != "" {
			manager.events.Emit("automation_evaluated", "-", map[string]any{
				"symbol": position.Symbol, "exit_decision": decision,
			})
		}
		if !decision.Trigger {
			continue
		}
		if exitErr := manager.exit(ctx, position, mark, decision); exitErr != nil {
			errorsSeen = append(errorsSeen, fmt.Errorf("%s automated exit: %w", position.Symbol, exitErr))
			continue
		}
		manager.model.Remove(position)
		manager.logger.InfoContext(ctx, "automated_exit_filled",
			"symbol", position.Symbol, "reason", decision.Reason,
			"current_r", decision.CurrentR, "peak_r", decision.PeakR)
	}
	for key := range manager.model.series {
		if _, exists := active[key]; !exists {
			delete(manager.model.series, key)
		}
	}
	return errors.Join(errorsSeen...)
}

func stopLossPrice(orders []contracts.ProtectiveOrder, position contracts.Position) (contracts.Decimal, bool) {
	for _, order := range orders {
		if order.Kind == "stop_loss" && order.ReduceOnly && order.TriggerPrice.IsPositive() &&
			(order.PositionSide == "" || order.PositionSide == position.Side) &&
			(order.FullClose || order.PositionSide == "" ||
				(order.SizeBase.IsPositive() && order.SizeBase.Cmp(position.SizeBase) >= 0)) {
			return order.TriggerPrice, true
		}
	}
	return contracts.Zero(), false
}

func (manager *AutomatedExitManager) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("automated exit context is required")
	}
	for {
		if err := manager.CheckOnce(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			manager.logger.ErrorContext(ctx, "automated_exit_cycle_failed", "error", err.Error())
		}
		timer := time.NewTimer(manager.interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}
