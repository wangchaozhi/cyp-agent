package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/events"
	"github.com/wangchaozhi/cyp-agent/internal/observability"
)

type AlertSender interface {
	Alert(ctx context.Context, level, message string, fields map[string]any) error
}

type MonitorReport struct {
	Positions []contracts.Position `json:"positions"`
	Alerts    []string             `json:"alerts"`
}

// MonitorVenue is read-only by construction and intentionally has no Place or
// Cancel method.
type MonitorVenue interface {
	ReconcileVenue
	FetchTicker(context.Context, string) (contracts.Decimal, error)
	Balances(context.Context) (contracts.Balances, error)
}

type PositionMonitorConfig struct {
	Venue          MonitorVenue
	Interval       time.Duration
	Events         *events.Bus
	Alerter        AlertSender
	Logger         *slog.Logger
	Metrics        *observability.RuntimeMetrics
	StopProximity  contracts.Decimal
	LiqProximity   contracts.Decimal
	MoveThreshold  contracts.Decimal
	MinMarginRatio contracts.Decimal
	Safety         *SafetyState
	EventHeartbeat time.Duration
	AlertCooldown  time.Duration
}

type PositionMonitor struct {
	venue                MonitorVenue
	interval             time.Duration
	events               *events.Bus
	alerter              AlertSender
	logger               *slog.Logger
	metrics              *observability.RuntimeMetrics
	stopProximity        contracts.Decimal
	liqProximity         contracts.Decimal
	moveThreshold        contracts.Decimal
	minMarginRatio       contracts.Decimal
	safety               *SafetyState
	eventHeartbeat       time.Duration
	alertCooldown        time.Duration
	now                  func() time.Time
	marksMu              sync.Mutex
	lastMarks            map[string]contracts.Decimal
	stateMu              sync.Mutex
	lastEventFingerprint string
	lastEventAt          time.Time
	lastAlertFingerprint string
	lastAlertAt          time.Time
	unsafeCounts         map[string]int
}

func NewPositionMonitor(config PositionMonitorConfig) (*PositionMonitor, error) {
	if config.Venue == nil {
		return nil, errors.New("monitor venue is required")
	}
	if config.Interval <= 0 {
		return nil, errors.New("monitor interval must be positive")
	}
	if config.Logger == nil {
		config.Logger = observability.DefaultLogger("monitor")
	}
	if config.StopProximity.IsZero() {
		config.StopProximity = contracts.MustDecimal("0.3")
	}
	if config.LiqProximity.IsZero() {
		config.LiqProximity = contracts.MustDecimal("0.10")
	}
	if config.MoveThreshold.IsZero() {
		config.MoveThreshold = contracts.MustDecimal("0.05")
	}
	if config.MinMarginRatio.IsZero() {
		config.MinMarginRatio = contracts.MustDecimal("0.05")
	}
	if config.EventHeartbeat <= 0 {
		config.EventHeartbeat = time.Minute
	}
	if config.AlertCooldown <= 0 {
		config.AlertCooldown = 5 * time.Minute
	}
	for name, threshold := range map[string]contracts.Decimal{
		"stop proximity": config.StopProximity, "liquidation proximity": config.LiqProximity,
		"move threshold": config.MoveThreshold, "minimum margin ratio": config.MinMarginRatio,
	} {
		if threshold.IsNegative() {
			return nil, fmt.Errorf("%s cannot be negative", name)
		}
	}
	return &PositionMonitor{
		venue: config.Venue, interval: config.Interval, events: config.Events,
		alerter: config.Alerter, logger: config.Logger, metrics: config.Metrics,
		stopProximity: config.StopProximity, liqProximity: config.LiqProximity,
		moveThreshold: config.MoveThreshold, minMarginRatio: config.MinMarginRatio,
		safety: config.Safety, eventHeartbeat: config.EventHeartbeat, alertCooldown: config.AlertCooldown,
		now: time.Now, lastMarks: make(map[string]contracts.Decimal), unsafeCounts: make(map[string]int),
	}, nil
}

func (monitor *PositionMonitor) CheckOnce(ctx context.Context) (report MonitorReport, err error) {
	report = MonitorReport{Positions: []contracts.Position{}, Alerts: []string{}}
	defer func() { monitor.metrics.RecordMonitor(err) }()
	if ctx == nil {
		return report, errors.New("monitor context is required")
	}
	if err := ValidateExecutionVenue(monitor.venue); err != nil {
		return report, err
	}
	positions, err := monitor.venue.Positions(ctx)
	if err != nil {
		return report, fmt.Errorf("load execution venue positions: %w", err)
	}
	report.Positions = append(report.Positions, positions...)
	activeSymbols := make(map[string]struct{}, len(positions))
	unsafeProtection := make(map[string]struct{})
	ordersBySymbol := make(map[string][]contracts.ProtectiveOrder)
	inspectableBySymbol := make(map[string]bool)
	inspectionFailedBySymbol := make(map[string]bool)
	for _, position := range positions {
		activeSymbols[position.Symbol] = struct{}{}
		orders, checked := ordersBySymbol[position.Symbol]
		inspectable := inspectableBySymbol[position.Symbol]
		if !checked {
			var inspectErr error
			orders, inspectable, inspectErr = inspectProtectiveOrders(ctx, monitor.venue, position.Symbol)
			if inspectErr != nil {
				report.Alerts = append(report.Alerts,
					fmt.Sprintf("%s 核验保护单失败：%s", position.Symbol, inspectErr.Error()))
				inspectionFailedBySymbol[position.Symbol] = true
				unsafeProtection[position.Symbol] = struct{}{}
			}
			ordersBySymbol[position.Symbol] = orders
			inspectableBySymbol[position.Symbol] = inspectable
		}
		if !monitor.venue.Caps().NativeProtectiveOrders {
			report.Alerts = append(report.Alerts,
				fmt.Sprintf("%s 无原生保护单，保护依赖监控存活", position.Symbol))
			unsafeProtection[position.Symbol] = struct{}{}
		} else if !inspectionFailedBySymbol[position.Symbol] && (!inspectable || !hasStopLoss(orders)) {
			report.Alerts = append(report.Alerts, fmt.Sprintf("%s 缺少止损保护单", position.Symbol))
			unsafeProtection[position.Symbol] = struct{}{}
		}
		mark, tickerErr := monitor.venue.FetchTicker(ctx, position.Symbol)
		if tickerErr != nil || !mark.IsPositive() {
			report.Alerts = append(report.Alerts, fmt.Sprintf("%s 无法获取有效 mark price", position.Symbol))
			continue
		}
		report.Alerts = append(report.Alerts, monitor.positionAlerts(position, mark, orders)...)
	}
	monitor.cleanupMarks(activeSymbols)
	monitor.updateProtectionSafety(activeSymbols, unsafeProtection)
	if marginAlert, marginErr := monitor.marginAlert(ctx, positions); marginErr != nil {
		monitor.logger.ErrorContext(ctx, "margin_monitor_failed", "error", marginErr.Error())
	} else if marginAlert != "" {
		report.Alerts = append(report.Alerts, marginAlert)
		if monitor.safety != nil {
			monitor.safety.Freeze("account margin safety threshold breached")
		}
	}
	// An empty healthy poll has no user-visible state change. Suppressing it
	// keeps the SSE timeline and browser rendering quiet while there are no
	// positions, without changing the monitor cadence or alerting behavior.
	if monitor.events != nil && (len(report.Positions) > 0 || len(report.Alerts) > 0) && monitor.shouldEmitEvent(report) {
		monitor.events.Emit("position_monitor", "-", map[string]any{
			"positions": report.Positions, "alerts": report.Alerts,
		})
	}
	if len(report.Alerts) > 0 && monitor.shouldSendAlert(report.Alerts) {
		monitor.logger.WarnContext(ctx, "position_alerts", "alerts", report.Alerts)
		if monitor.alerter != nil {
			if alertErr := monitor.alerter.Alert(ctx, "warning", "position_monitor", map[string]any{
				"alerts": report.Alerts,
			}); alertErr != nil {
				monitor.logger.ErrorContext(ctx, "position_alert_delivery_failed", "error", alertErr.Error())
			}
		}
	} else if len(report.Alerts) == 0 {
		monitor.clearAlertState()
	}
	return report, nil
}

func (monitor *PositionMonitor) shouldEmitEvent(report MonitorReport) bool {
	raw, _ := json.Marshal(report)
	fingerprint := string(raw)
	now := monitor.now().UTC()
	monitor.stateMu.Lock()
	defer monitor.stateMu.Unlock()
	if fingerprint == monitor.lastEventFingerprint && now.Sub(monitor.lastEventAt) < monitor.eventHeartbeat {
		return false
	}
	monitor.lastEventFingerprint = fingerprint
	monitor.lastEventAt = now
	return true
}

func (monitor *PositionMonitor) shouldSendAlert(alerts []string) bool {
	raw, _ := json.Marshal(alerts)
	fingerprint := string(raw)
	now := monitor.now().UTC()
	monitor.stateMu.Lock()
	defer monitor.stateMu.Unlock()
	if fingerprint == monitor.lastAlertFingerprint && now.Sub(monitor.lastAlertAt) < monitor.alertCooldown {
		return false
	}
	monitor.lastAlertFingerprint = fingerprint
	monitor.lastAlertAt = now
	return true
}

func (monitor *PositionMonitor) clearAlertState() {
	monitor.stateMu.Lock()
	monitor.lastAlertFingerprint = ""
	monitor.lastAlertAt = time.Time{}
	monitor.stateMu.Unlock()
}

func (monitor *PositionMonitor) cleanupMarks(active map[string]struct{}) {
	monitor.marksMu.Lock()
	defer monitor.marksMu.Unlock()
	for symbol := range monitor.lastMarks {
		if _, exists := active[symbol]; !exists {
			delete(monitor.lastMarks, symbol)
		}
	}
}

func (monitor *PositionMonitor) updateProtectionSafety(active, unsafe map[string]struct{}) {
	monitor.stateMu.Lock()
	freezeSymbol := ""
	for symbol := range active {
		if _, missing := unsafe[symbol]; missing {
			monitor.unsafeCounts[symbol]++
			if monitor.unsafeCounts[symbol] >= 2 && freezeSymbol == "" {
				freezeSymbol = symbol
			}
		} else {
			delete(monitor.unsafeCounts, symbol)
		}
	}
	for symbol := range monitor.unsafeCounts {
		if _, exists := active[symbol]; !exists {
			delete(monitor.unsafeCounts, symbol)
		}
	}
	monitor.stateMu.Unlock()
	if freezeSymbol != "" && monitor.safety != nil {
		monitor.safety.Freeze("position protection gap: " + freezeSymbol)
	}
}

func (monitor *PositionMonitor) positionAlerts(
	position contracts.Position,
	mark contracts.Decimal,
	orders []contracts.ProtectiveOrder,
) []string {
	result := make([]string, 0)
	monitor.marksMu.Lock()
	last, hadLast := monitor.lastMarks[position.Symbol]
	monitor.lastMarks[position.Symbol] = mark
	monitor.marksMu.Unlock()
	if hadLast && last.IsPositive() {
		if movement, err := mark.Sub(last).Abs().Quo(last); err == nil && movement.Cmp(monitor.moveThreshold) >= 0 {
			result = append(result, fmt.Sprintf("%s 异常波动 %s（%s → %s）",
				position.Symbol, formatPercent(movement), last.String(), mark.String()))
		}
	}
	for _, order := range orders {
		if order.Kind != "stop_loss" || !order.TriggerPrice.IsPositive() {
			continue
		}
		full := position.EntryPrice.Sub(order.TriggerPrice).Abs()
		if !full.IsPositive() {
			continue
		}
		remaining, err := mark.Sub(order.TriggerPrice).Abs().Quo(full)
		if err == nil && remaining.Cmp(monitor.stopProximity) <= 0 {
			result = append(result, fmt.Sprintf("%s 价格 %s 已逼近止损 %s",
				position.Symbol, mark.String(), order.TriggerPrice.String()))
		}
	}
	if position.Instrument == contracts.InstrumentPerp && position.LiqPrice != nil && position.LiqPrice.IsPositive() {
		distance, err := mark.Sub(*position.LiqPrice).Abs().Quo(mark)
		if err == nil && distance.Cmp(monitor.liqProximity) <= 0 {
			result = append(result, fmt.Sprintf("%s 价格 %s 距爆仓价 %s 仅 %s",
				position.Symbol, mark.String(), position.LiqPrice.String(), formatPercent(distance)))
		}
	}
	return result
}

func (monitor *PositionMonitor) marginAlert(
	ctx context.Context,
	positions []contracts.Position,
) (string, error) {
	perpNotional := contracts.Zero()
	for _, position := range positions {
		if position.Instrument != contracts.InstrumentPerp {
			continue
		}
		monitor.marksMu.Lock()
		mark, ok := monitor.lastMarks[position.Symbol]
		monitor.marksMu.Unlock()
		if !ok || !mark.IsPositive() {
			mark = position.EntryPrice
		}
		perpNotional = perpNotional.Add(position.NotionalAt(mark))
	}
	if !perpNotional.IsPositive() {
		return "", nil
	}
	balances, err := monitor.venue.Balances(ctx)
	if err != nil {
		return "", err
	}
	equity := balances.TotalQuote
	if !equity.IsPositive() {
		equity = balances.FreeQuote
	}
	ratio, err := equity.Quo(perpNotional)
	if err != nil {
		return "", err
	}
	threshold := monitor.minMarginRatio.Mul(contracts.NewDecimalFromInt64(2))
	if ratio.Cmp(threshold) < 0 {
		return fmt.Sprintf("保证金率 %s 逼近下限 %s", formatPercent(ratio), formatPercent(monitor.minMarginRatio)), nil
	}
	return "", nil
}

func formatPercent(value contracts.Decimal) string {
	number, err := value.Float64()
	if err != nil {
		return value.String()
	}
	return fmt.Sprintf("%.2f%%", number*100)
}

func (monitor *PositionMonitor) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("monitor context is required")
	}
	for {
		if _, err := monitor.CheckOnce(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			monitor.logger.ErrorContext(ctx, "monitor_cycle_failed", "error", err.Error())
		}
		timer := time.NewTimer(monitor.interval)
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

func (monitor *PositionMonitor) RunCycles(ctx context.Context, cycles int) error {
	if cycles < 0 {
		return errors.New("monitor cycles cannot be negative")
	}
	errorsSeen := make([]error, 0)
	for cycle := 0; cycle < cycles; cycle++ {
		if _, err := monitor.CheckOnce(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			errorsSeen = append(errorsSeen, err)
		}
	}
	return errors.Join(errorsSeen...)
}
