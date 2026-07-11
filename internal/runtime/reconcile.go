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

type VenueReconciler struct {
	venue  ReconcileVenue
	events *events.Bus
	logger *slog.Logger
}

func NewVenueReconciler(target ReconcileVenue, eventBus *events.Bus, logger *slog.Logger) (*VenueReconciler, error) {
	if target == nil {
		return nil, errors.New("reconcile venue is required")
	}
	if logger == nil {
		logger = observability.DefaultLogger("reconcile")
	}
	return &VenueReconciler{venue: target, events: eventBus, logger: logger}, nil
}

func (reconciler *VenueReconciler) Reconcile(ctx context.Context) (ReconcileReport, error) {
	report := ReconcileReport{
		Positions: []contracts.Position{}, Discrepancies: []string{}, ProtectiveGaps: []string{},
	}
	if ctx == nil {
		return report, errors.New("reconcile context is required")
	}
	if err := ValidatePaperVenue(string(reconciler.venue.Kind()), reconciler.venue.ID()); err != nil {
		return report, err
	}
	positions, err := reconciler.venue.Positions(ctx)
	if err != nil {
		return report, fmt.Errorf("load paper positions: %w", err)
	}
	report.Positions = append(report.Positions, positions...)
	reader, canInspectProtective := reconciler.venue.(protectiveReader)
	checkedSymbols := make(map[string]struct{})
	for _, position := range positions {
		if _, checked := checkedSymbols[position.Symbol]; checked {
			continue
		}
		checkedSymbols[position.Symbol] = struct{}{}
		switch {
		case !reconciler.venue.Caps().NativeProtectiveOrders:
			report.ProtectiveGaps = append(report.ProtectiveGaps,
				fmt.Sprintf("%s 无原生保护单，保护依赖监控存活", position.Symbol))
		case !canInspectProtective:
			report.ProtectiveGaps = append(report.ProtectiveGaps,
				fmt.Sprintf("%s 无法核验原生保护单", position.Symbol))
		case !hasStopLoss(reader.ProtectiveFor(position.Symbol)):
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

func hasStopLoss(orders []contracts.ProtectiveOrder) bool {
	for _, order := range orders {
		if strings.EqualFold(order.Kind, "stop_loss") && order.ReduceOnly && order.TriggerPrice.IsPositive() {
			return true
		}
	}
	return false
}

var _ Reconciler = (*VenueReconciler)(nil)
