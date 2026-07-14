package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/events"
	"github.com/wangchaozhi/cyp-agent/internal/observability"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

type ReconcileReport struct {
	Positions      []contracts.Position `json:"positions"`
	Discrepancies  []string             `json:"discrepancies"`
	ProtectiveGaps []string             `json:"protective_gaps"`
	OK             bool                 `json:"ok"`
}

type Reconciler interface {
	Reconcile(ctx context.Context) (ReconcileReport, error)
}

type OrderStateReconciler interface {
	ReconcileOrders(context.Context, []contracts.Position) ([]string, error)
}

// ReconcileVenue deliberately excludes Place/Cancel/Preflight. Startup
// reconciliation is a read-only safety operation.
type ReconcileVenue interface {
	ID() string
	Kind() venue.Kind
	Caps() venue.Caps
	Positions(context.Context) ([]contracts.Position, error)
}

type protectiveReader interface {
	ProtectiveFor(symbol string) []contracts.ProtectiveOrder
}

type remoteProtectiveReader interface {
	ProtectiveOrders(context.Context, string) ([]contracts.ProtectiveOrder, error)
}

type reconcileAccountReader interface {
	Balances(context.Context) (contracts.Balances, error)
	FetchTicker(context.Context, string) (contracts.Decimal, error)
}

type VenueReconciler struct {
	venue         ReconcileVenue
	events        *events.Bus
	logger        *slog.Logger
	positionState interface {
		ReconcilePositions(context.Context, []contracts.Position) error
	}
	orderState     OrderStateReconciler
	minMarginRatio contracts.Decimal
}

type ReconcileOption func(*VenueReconciler)

func WithPositionState(state interface {
	ReconcilePositions(context.Context, []contracts.Position) error
}) ReconcileOption {
	return func(reconciler *VenueReconciler) { reconciler.positionState = state }
}

func WithOrderState(state OrderStateReconciler) ReconcileOption {
	return func(reconciler *VenueReconciler) { reconciler.orderState = state }
}

func WithMinimumMarginRatio(value contracts.Decimal) ReconcileOption {
	return func(reconciler *VenueReconciler) { reconciler.minMarginRatio = value }
}

func NewVenueReconciler(target ReconcileVenue, eventBus *events.Bus, logger *slog.Logger, options ...ReconcileOption) (*VenueReconciler, error) {
	if target == nil {
		return nil, errors.New("reconcile venue is required")
	}
	if logger == nil {
		logger = observability.DefaultLogger("reconcile")
	}
	reconciler := &VenueReconciler{venue: target, events: eventBus, logger: logger}
	for _, option := range options {
		option(reconciler)
	}
	return reconciler, nil
}

func (reconciler *VenueReconciler) Reconcile(ctx context.Context) (ReconcileReport, error) {
	report := ReconcileReport{
		Positions: []contracts.Position{}, Discrepancies: []string{}, ProtectiveGaps: []string{},
	}
	if ctx == nil {
		return report, errors.New("reconcile context is required")
	}
	if err := ValidateExecutionVenue(reconciler.venue); err != nil {
		return report, err
	}
	positions, err := reconciler.venue.Positions(ctx)
	if err != nil {
		return report, fmt.Errorf("load execution venue positions: %w", err)
	}
	report.Positions = append(report.Positions, positions...)
	if reconciler.positionState != nil {
		if stateErr := reconciler.positionState.ReconcilePositions(ctx, positions); stateErr != nil {
			report.Discrepancies = append(report.Discrepancies,
				"持仓风险账本对账失败："+stateErr.Error())
		}
	}
	if reconciler.orderState != nil {
		orderDiscrepancies, orderErr := reconciler.orderState.ReconcileOrders(ctx, positions)
		if orderErr != nil {
			return report, fmt.Errorf("reconcile durable orders: %w", orderErr)
		}
		report.Discrepancies = append(report.Discrepancies, orderDiscrepancies...)
	}
	if reconciler.minMarginRatio.IsPositive() {
		marginDiscrepancies, marginErr := reconciler.reconcileMargin(ctx, positions)
		if marginErr != nil {
			return report, marginErr
		}
		report.Discrepancies = append(report.Discrepancies, marginDiscrepancies...)
	}
	checkedSymbols := make(map[string]struct{})
	for _, position := range positions {
		if _, checked := checkedSymbols[position.Symbol]; checked {
			continue
		}
		checkedSymbols[position.Symbol] = struct{}{}
		if !reconciler.venue.Caps().NativeProtectiveOrders {
			report.ProtectiveGaps = append(report.ProtectiveGaps,
				fmt.Sprintf("%s 无原生保护单，保护依赖监控存活", position.Symbol))
			continue
		}
		orders, inspectable, inspectErr := inspectProtectiveOrders(ctx, reconciler.venue, position.Symbol)
		if inspectErr != nil {
			report.Discrepancies = append(report.Discrepancies,
				fmt.Sprintf("%s 核验保护单失败：%s", position.Symbol, inspectErr.Error()))
			continue
		}
		switch {
		case !inspectable:
			report.ProtectiveGaps = append(report.ProtectiveGaps,
				fmt.Sprintf("%s 无法核验原生保护单", position.Symbol))
		case !hasStopLossForPosition(orders, position):
			report.ProtectiveGaps = append(report.ProtectiveGaps,
				fmt.Sprintf("%s 缺少止损保护单", position.Symbol))
		}
	}
	report.OK = len(report.Discrepancies) == 0 && len(report.ProtectiveGaps) == 0
	reconciler.logger.InfoContext(ctx, "reconciled",
		"positions", len(report.Positions), "gaps", len(report.ProtectiveGaps), "ok", report.OK)
	if reconciler.events != nil {
		reconciler.events.Emit("reconciled", "-", map[string]any{
			"positions": report.Positions, "protective_gaps": report.ProtectiveGaps,
			"discrepancies": report.Discrepancies, "ok": report.OK,
		})
	}
	return report, nil
}

func (reconciler *VenueReconciler) reconcileMargin(ctx context.Context, positions []contracts.Position) ([]string, error) {
	reader, ok := reconciler.venue.(reconcileAccountReader)
	if !ok {
		return []string{"执行场所无法核验账户保证金"}, nil
	}
	balances, err := reader.Balances(ctx)
	if err != nil {
		return nil, fmt.Errorf("load balances for reconciliation: %w", err)
	}
	equity := balances.TotalQuote
	if !equity.IsPositive() {
		equity = balances.FreeQuote
	}
	perpetualNotional := contracts.Zero()
	for _, position := range positions {
		if position.Instrument != contracts.InstrumentPerp {
			continue
		}
		mark, markErr := reader.FetchTicker(ctx, position.Symbol)
		if markErr != nil || !mark.IsPositive() {
			mark = position.EntryPrice
		}
		perpetualNotional = perpetualNotional.Add(position.NotionalAt(mark))
	}
	if !perpetualNotional.IsPositive() {
		return []string{}, nil
	}
	ratio, err := equity.Quo(perpetualNotional)
	if err != nil {
		return nil, fmt.Errorf("calculate reconciliation margin ratio: %w", err)
	}
	if ratio.Cmp(reconciler.minMarginRatio) < 0 {
		return []string{fmt.Sprintf("账户保证金率 %s 低于安全阈值 %s", ratio.String(), reconciler.minMarginRatio.String())}, nil
	}
	return []string{}, nil
}

func inspectProtectiveOrders(
	ctx context.Context,
	target ReconcileVenue,
	symbol string,
) ([]contracts.ProtectiveOrder, bool, error) {
	if reader, ok := target.(protectiveReader); ok {
		return reader.ProtectiveFor(symbol), true, nil
	}
	if reader, ok := target.(remoteProtectiveReader); ok {
		orders, err := reader.ProtectiveOrders(ctx, symbol)
		return orders, true, err
	}
	return nil, false, nil
}

func hasStopLossForPosition(orders []contracts.ProtectiveOrder, position contracts.Position) bool {
	for _, order := range orders {
		if !strings.EqualFold(order.Kind, "stop_loss") || !order.ReduceOnly || !order.TriggerPrice.IsPositive() {
			continue
		}
		if order.PositionSide != "" && order.PositionSide != position.Side {
			continue
		}
		if order.FullClose || (order.SizeBase.IsPositive() && order.SizeBase.Cmp(position.SizeBase) >= 0) {
			return true
		}
		// Legacy/Paper readers did not expose coverage metadata. OKX readers
		// always expose PositionSide, so only metadata-free local orders use
		// this compatibility path.
		if order.PositionSide == "" {
			return true
		}
	}
	return false
}

var _ Reconciler = (*VenueReconciler)(nil)
