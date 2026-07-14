// Package riskstate maintains the durable account statistics consumed by the
// deterministic risk engine. It deliberately sits outside the execution venue
// so risk limits survive process restarts and remain independently auditable.
package riskstate

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

const (
	checkpointRunID = "__system_risk_state__"
	checkpointStep  = "current"
	maxEquityPoints = 512
	maxTrades       = 1000
	minCVaRSamples  = 20
)

// Repository is the narrow durable checkpoint boundary needed by Tracker.
type Repository interface {
	SaveCheckpoint(context.Context, string, string, any) error
	LoadCheckpoints(context.Context, string) (map[string]json.RawMessage, error)
}

type EquityPoint struct {
	TS     time.Time         `json:"ts"`
	Equity contracts.Decimal `json:"equity"`
}

type TradeRecord struct {
	ClientID   string               `json:"client_id"`
	RunID      string               `json:"run_id,omitempty"`
	Kind       string               `json:"kind"`
	Symbol     string               `json:"symbol"`
	Side       contracts.Side       `json:"side"`
	Instrument contracts.Instrument `json:"instrument"`
	Price      contracts.Decimal    `json:"price"`
	SizeBase   contracts.Decimal    `json:"size_base"`
	FeeQuote   contracts.Decimal    `json:"fee_quote"`
	PNLQuote   contracts.Decimal    `json:"pnl_quote"`
	TS         time.Time            `json:"ts"`
}

type persistedState struct {
	Version           int               `json:"version"`
	InitialEquity     contracts.Decimal `json:"initial_equity"`
	CurrentEquity     contracts.Decimal `json:"current_equity"`
	PeakEquity        contracts.Decimal `json:"peak_equity"`
	DayStartEquity    contracts.Decimal `json:"day_start_equity"`
	WeekStartEquity   contracts.Decimal `json:"week_start_equity"`
	Day               string            `json:"day"`
	Week              string            `json:"week"`
	ConsecutiveLosses int               `json:"consecutive_losses"`
	RealizedPNL       contracts.Decimal `json:"realized_pnl"`
	Orders            []time.Time       `json:"orders"`
	EquityPoints      []EquityPoint     `json:"equity_points"`
	Trades            []TradeRecord     `json:"trades"`
}

// Snapshot is an immutable view suitable for both the risk engine and API.
type Snapshot struct {
	CurrentEquity      contracts.Decimal  `json:"current_equity"`
	DailyDrawdown      contracts.Decimal  `json:"daily_drawdown"`
	WeeklyDrawdown     contracts.Decimal  `json:"weekly_drawdown"`
	TotalDrawdown      contracts.Decimal  `json:"total_drawdown"`
	OrdersLastHour     int                `json:"orders_last_hour"`
	ConsecutiveLosses  int                `json:"consecutive_losses"`
	RealizedPNL        contracts.Decimal  `json:"realized_pnl"`
	PortfolioCVARQuote *contracts.Decimal `json:"portfolio_cvar_quote,omitempty"`
	CVaRSamples        int                `json:"cvar_samples"`
}

type Tracker struct {
	mu         sync.RWMutex
	repository Repository
	now        func() time.Time
	step       string
	state      persistedState
}

func New(ctx context.Context, repository Repository, initialEquity contracts.Decimal) (*Tracker, error) {
	return NewScoped(ctx, repository, initialEquity, "")
}

// NewScoped isolates durable risk baselines between execution accounts. The
// local Paper scope may import the legacy unscoped checkpoint; Demo and Live
// scopes deliberately have no fallback, so Paper equity can never pollute them.
func NewScoped(
	ctx context.Context,
	repository Repository,
	initialEquity contracts.Decimal,
	scope string,
) (*Tracker, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !initialEquity.IsPositive() {
		return nil, errors.New("risk state initial equity must be positive")
	}
	now := time.Now().UTC()
	normalizedScope := normalizeScope(scope)
	step := checkpointStep
	if normalizedScope != "" {
		step += ":" + normalizedScope
	}
	tracker := &Tracker{repository: repository, now: time.Now, step: step}
	tracker.state = newState(initialEquity, now)
	if repository == nil {
		return tracker, nil
	}
	checkpoints, err := repository.LoadCheckpoints(ctx, checkpointRunID)
	if err != nil {
		return nil, err
	}
	raw := checkpoints[step]
	if len(raw) == 0 && normalizedScope == "paper" {
		raw = checkpoints[checkpointStep]
	}
	if len(raw) > 0 {
		var restored persistedState
		if err := json.Unmarshal(raw, &restored); err != nil {
			return nil, err
		}
		if restored.Version == 1 && restored.InitialEquity.IsPositive() {
			tracker.state = normalizeState(restored, initialEquity, now)
		}
	}
	return tracker, nil
}

func normalizeScope(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			result.WriteRune(character)
		} else if result.Len() > 0 {
			result.WriteByte('-')
		}
	}
	return strings.Trim(result.String(), "-")
}

func newState(initial contracts.Decimal, now time.Time) persistedState {
	return persistedState{
		Version: 1, InitialEquity: initial, CurrentEquity: initial, PeakEquity: initial,
		DayStartEquity: initial, WeekStartEquity: initial, Day: dayKey(now), Week: weekKey(now),
		Orders: []time.Time{}, EquityPoints: []EquityPoint{{TS: now, Equity: initial}}, Trades: []TradeRecord{},
	}
}

func normalizeState(state persistedState, fallback contracts.Decimal, now time.Time) persistedState {
	if !state.CurrentEquity.IsPositive() {
		state.CurrentEquity = fallback
	}
	if !state.PeakEquity.IsPositive() {
		state.PeakEquity = state.CurrentEquity
	}
	if !state.DayStartEquity.IsPositive() {
		state.DayStartEquity = state.CurrentEquity
	}
	if !state.WeekStartEquity.IsPositive() {
		state.WeekStartEquity = state.CurrentEquity
	}
	if state.Day == "" {
		state.Day = dayKey(now)
	}
	if state.Week == "" {
		state.Week = weekKey(now)
	}
	if state.Orders == nil {
		state.Orders = []time.Time{}
	}
	if state.EquityPoints == nil {
		state.EquityPoints = []EquityPoint{}
	}
	if state.Trades == nil {
		state.Trades = []TradeRecord{}
	}
	return state
}

// ObserveEquity updates peak and period baselines, then persists atomically.
func (tracker *Tracker) ObserveEquity(ctx context.Context, equity contracts.Decimal) error {
	if tracker == nil || !equity.IsPositive() {
		return nil
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	before := clonePersistedState(tracker.state)
	now := tracker.now().UTC()
	tracker.observeLocked(equity, now)
	if err := tracker.persistLocked(ctx); err != nil {
		tracker.state = before
		return err
	}
	return nil
}

func (tracker *Tracker) RecordOpen(
	ctx context.Context,
	runID string,
	proposal contracts.TradeProposal,
	execution contracts.ExecutionResult,
	equity contracts.Decimal,
) error {
	if tracker == nil || !execution.FilledBase.IsPositive() ||
		(execution.Status != contracts.OrderStatusFilled && execution.Status != contracts.OrderStatusPartiallyFilled) {
		return nil
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.hasTradeLocked(execution.ClientID, "open") {
		return nil
	}
	before := clonePersistedState(tracker.state)
	now := tracker.now().UTC()
	price := contracts.Zero()
	if execution.AvgPrice != nil {
		price = *execution.AvgPrice
	}
	tracker.state.RealizedPNL = tracker.state.RealizedPNL.Sub(execution.FeeQuote)
	tracker.state.Trades = append(tracker.state.Trades, TradeRecord{
		ClientID: execution.ClientID, RunID: runID, Kind: "open", Symbol: proposal.Symbol,
		Side: proposal.Side, Instrument: proposal.Instrument, Price: price,
		SizeBase: execution.FilledBase, FeeQuote: execution.FeeQuote,
		PNLQuote: execution.FeeQuote.Neg(), TS: now,
	})
	tracker.recordOrderLocked(now)
	tracker.observeLocked(equity, now)
	tracker.trimLocked()
	if err := tracker.persistLocked(ctx); err != nil {
		tracker.state = before
		return err
	}
	return nil
}

func (tracker *Tracker) RecordClose(
	ctx context.Context,
	reference string,
	position contracts.Position,
	execution contracts.ExecutionResult,
	equity contracts.Decimal,
) (TradeRecord, error) {
	if tracker == nil || execution.Status != contracts.OrderStatusFilled || execution.AvgPrice == nil {
		return TradeRecord{}, nil
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.hasTradeLocked(execution.ClientID, "close") {
		for _, trade := range tracker.state.Trades {
			if trade.ClientID == execution.ClientID && trade.Kind == "close" {
				return trade, nil
			}
		}
	}
	before := clonePersistedState(tracker.state)
	now := tracker.now().UTC()
	pnl := position.SizeBase.Mul(execution.AvgPrice.Sub(position.EntryPrice))
	if position.Side == contracts.SideShort {
		pnl = pnl.Neg()
	}
	pnl = pnl.Sub(execution.FeeQuote)
	tracker.state.RealizedPNL = tracker.state.RealizedPNL.Add(pnl)
	if pnl.IsNegative() {
		tracker.state.ConsecutiveLosses++
	} else if pnl.IsPositive() {
		tracker.state.ConsecutiveLosses = 0
	}
	record := TradeRecord{
		ClientID: execution.ClientID, RunID: reference, Kind: "close", Symbol: position.Symbol,
		Side: position.Side, Instrument: position.Instrument, Price: *execution.AvgPrice,
		SizeBase: execution.FilledBase, FeeQuote: execution.FeeQuote, PNLQuote: pnl, TS: now,
	}
	tracker.state.Trades = append(tracker.state.Trades, record)
	tracker.recordOrderLocked(now)
	tracker.observeLocked(equity, now)
	tracker.trimLocked()
	if err := tracker.persistLocked(ctx); err != nil {
		tracker.state = before
		return record, err
	}
	return record, nil
}

func (tracker *Tracker) OpenTrade(symbol string, instrument contracts.Instrument) (TradeRecord, bool) {
	if tracker == nil {
		return TradeRecord{}, false
	}
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	for index := len(tracker.state.Trades) - 1; index >= 0; index-- {
		trade := tracker.state.Trades[index]
		if trade.Symbol != symbol || trade.Instrument != instrument {
			continue
		}
		if closesPosition(trade.Kind) {
			return TradeRecord{}, false
		}
		if opensPosition(trade.Kind) {
			return trade, true
		}
	}
	return TradeRecord{}, false
}

// ReconcilePositions repairs the durable open/flat markers from the execution
// venue during startup. It never invents realized PnL for activity that
// happened while the process was offline; reconciliation records are explicit
// in the ledger so operators can distinguish them from observed fills.
func (tracker *Tracker) ReconcilePositions(ctx context.Context, positions []contracts.Position) error {
	if tracker == nil {
		return nil
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	type openState struct {
		trade TradeRecord
		open  bool
	}
	states := make(map[string]openState)
	for _, trade := range tracker.state.Trades {
		key := positionKey(trade.Symbol, trade.Instrument)
		if opensPosition(trade.Kind) {
			states[key] = openState{trade: trade, open: true}
		} else if closesPosition(trade.Kind) {
			states[key] = openState{trade: trade, open: false}
		}
	}
	active := make(map[string]contracts.Position, len(positions))
	for _, position := range positions {
		active[positionKey(position.Symbol, position.Instrument)] = position
	}
	now := tracker.now().UTC()
	sequence := 0
	before := clonePersistedState(tracker.state)
	changed := false
	for key, state := range states {
		if !state.open {
			continue
		}
		position, exists := active[key]
		if exists && positionMatchesTrade(position, state.trade) {
			continue
		}
		sequence++
		tracker.state.Trades = append(tracker.state.Trades, TradeRecord{
			ClientID: reconcileClientID("close", now, sequence), RunID: state.trade.RunID,
			Kind: "reconcile_close", Symbol: state.trade.Symbol, Side: state.trade.Side,
			Instrument: state.trade.Instrument, Price: state.trade.Price,
			SizeBase: state.trade.SizeBase, FeeQuote: contracts.Zero(), PNLQuote: contracts.Zero(), TS: now,
		})
		changed = true
	}
	for key, position := range active {
		if state := states[key]; state.open && positionMatchesTrade(position, state.trade) {
			continue
		}
		sequence++
		tracker.state.Trades = append(tracker.state.Trades, TradeRecord{
			ClientID: reconcileClientID("open", now, sequence), RunID: "reconciled",
			Kind: "reconcile_open", Symbol: position.Symbol, Side: position.Side,
			Instrument: position.Instrument, Price: position.EntryPrice,
			SizeBase: position.SizeBase, FeeQuote: contracts.Zero(), PNLQuote: contracts.Zero(), TS: now,
		})
		changed = true
	}
	if !changed {
		return nil
	}
	tracker.trimLocked()
	if err := tracker.persistLocked(ctx); err != nil {
		tracker.state = before
		return err
	}
	return nil
}

func positionKey(symbol string, instrument contracts.Instrument) string {
	return strings.ToUpper(strings.TrimSpace(symbol)) + "\x00" + string(instrument)
}

func positionMatchesTrade(position contracts.Position, trade TradeRecord) bool {
	return position.Side == trade.Side && position.SizeBase.Cmp(trade.SizeBase) == 0 &&
		position.EntryPrice.Cmp(trade.Price) == 0
}

func opensPosition(kind string) bool  { return kind == "open" || kind == "reconcile_open" }
func closesPosition(kind string) bool { return kind == "close" || kind == "reconcile_close" }

func reconcileClientID(kind string, now time.Time, sequence int) string {
	return "reconcile-" + kind + "-" + strconv.FormatInt(now.UnixNano(), 36) + "-" + strconv.Itoa(sequence)
}

func (tracker *Tracker) Snapshot(equity contracts.Decimal) Snapshot {
	if tracker == nil {
		return Snapshot{CurrentEquity: equity}
	}
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	state := tracker.state
	if equity.IsPositive() {
		state.CurrentEquity = equity
	}
	now := tracker.now().UTC()
	if dayKey(now) != state.Day {
		state.DayStartEquity = state.CurrentEquity
	}
	if weekKey(now) != state.Week {
		state.WeekStartEquity = state.CurrentEquity
	}
	if state.CurrentEquity.Cmp(state.PeakEquity) > 0 {
		state.PeakEquity = state.CurrentEquity
	}
	result := Snapshot{
		CurrentEquity:     state.CurrentEquity,
		DailyDrawdown:     drawdown(state.DayStartEquity, state.CurrentEquity),
		WeeklyDrawdown:    drawdown(state.WeekStartEquity, state.CurrentEquity),
		TotalDrawdown:     drawdown(state.PeakEquity, state.CurrentEquity),
		ConsecutiveLosses: state.ConsecutiveLosses, RealizedPNL: state.RealizedPNL,
	}
	cutoff := now.Add(-time.Hour)
	for _, order := range state.Orders {
		if !order.Before(cutoff) {
			result.OrdersLastHour++
		}
	}
	result.PortfolioCVARQuote, result.CVaRSamples = empiricalCVAR(state.EquityPoints, state.CurrentEquity)
	return result
}

func (tracker *Tracker) Trades() []TradeRecord {
	if tracker == nil {
		return []TradeRecord{}
	}
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	return append([]TradeRecord(nil), tracker.state.Trades...)
}

func (tracker *Tracker) observeLocked(equity contracts.Decimal, now time.Time) {
	if dayKey(now) != tracker.state.Day {
		tracker.state.Day, tracker.state.DayStartEquity = dayKey(now), equity
	}
	if weekKey(now) != tracker.state.Week {
		tracker.state.Week, tracker.state.WeekStartEquity = weekKey(now), equity
	}
	tracker.state.CurrentEquity = equity
	if equity.Cmp(tracker.state.PeakEquity) > 0 {
		tracker.state.PeakEquity = equity
	}
	points := tracker.state.EquityPoints
	if len(points) == 0 || points[len(points)-1].Equity.Cmp(equity) != 0 {
		tracker.state.EquityPoints = append(points, EquityPoint{TS: now, Equity: equity})
	}
	tracker.trimLocked()
}

func (tracker *Tracker) recordOrderLocked(now time.Time) {
	cutoff := now.Add(-time.Hour)
	kept := tracker.state.Orders[:0]
	for _, order := range tracker.state.Orders {
		if !order.Before(cutoff) {
			kept = append(kept, order)
		}
	}
	tracker.state.Orders = append(kept, now)
}

func (tracker *Tracker) hasTradeLocked(clientID, kind string) bool {
	for index := len(tracker.state.Trades) - 1; index >= 0; index-- {
		trade := tracker.state.Trades[index]
		if trade.ClientID == clientID && trade.Kind == kind {
			return true
		}
	}
	return false
}

func (tracker *Tracker) trimLocked() {
	if overflow := len(tracker.state.EquityPoints) - maxEquityPoints; overflow > 0 {
		tracker.state.EquityPoints = append([]EquityPoint(nil), tracker.state.EquityPoints[overflow:]...)
	}
	if overflow := len(tracker.state.Trades) - maxTrades; overflow > 0 {
		tracker.state.Trades = append([]TradeRecord(nil), tracker.state.Trades[overflow:]...)
	}
}

func (tracker *Tracker) persistLocked(ctx context.Context) error {
	if tracker.repository == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return tracker.repository.SaveCheckpoint(ctx, checkpointRunID, tracker.step, tracker.state)
}

func clonePersistedState(state persistedState) persistedState {
	copy := state
	copy.Orders = append([]time.Time(nil), state.Orders...)
	copy.EquityPoints = append([]EquityPoint(nil), state.EquityPoints...)
	copy.Trades = append([]TradeRecord(nil), state.Trades...)
	return copy
}

func drawdown(baseline, equity contracts.Decimal) contracts.Decimal {
	if !baseline.IsPositive() || equity.Cmp(baseline) >= 0 {
		return contracts.Zero()
	}
	value, err := baseline.Sub(equity).Quo(baseline)
	if err != nil || value.IsNegative() {
		return contracts.Zero()
	}
	return value
}

func empiricalCVAR(points []EquityPoint, equity contracts.Decimal) (*contracts.Decimal, int) {
	losses := make([]float64, 0, max(0, len(points)-1))
	for index := 1; index < len(points); index++ {
		previous, previousErr := points[index-1].Equity.Float64()
		current, currentErr := points[index].Equity.Float64()
		if previousErr != nil || currentErr != nil || previous <= 0 {
			continue
		}
		loss := math.Max(0, (previous-current)/previous)
		losses = append(losses, loss)
	}
	if len(losses) < minCVaRSamples {
		return nil, len(losses)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(losses)))
	tail := int(math.Ceil(float64(len(losses)) * 0.05))
	if tail < 1 {
		tail = 1
	}
	total := 0.0
	for _, loss := range losses[:tail] {
		total += loss
	}
	ratio := total / float64(tail)
	value, err := contracts.ParseDecimal(strconv.FormatFloat(ratio, 'g', -1, 64))
	if err != nil {
		return nil, len(losses)
	}
	quote := equity.Mul(value)
	return &quote, len(losses)
}

func dayKey(value time.Time) string { return value.UTC().Format("2006-01-02") }

func weekKey(value time.Time) string {
	value = value.UTC()
	weekday := (int(value.Weekday()) + 6) % 7
	return value.AddDate(0, 0, -weekday).Format("2006-01-02")
}
