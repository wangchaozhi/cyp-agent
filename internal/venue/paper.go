package venue

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

var (
	ErrMarkPriceUnavailable = errors.New("paper venue mark price is unavailable")
	ErrInvalidMarkPrice     = errors.New("paper venue mark price must be positive")
	ErrClientIDRequired     = errors.New("paper venue client_id is required")
)

var (
	decimalOne         = contracts.NewDecimalFromInt64(1)
	decimalTenThousand = contracts.NewDecimalFromInt64(10_000)
)

type positionKey struct {
	symbol     string
	instrument contracts.Instrument
}

// PaperOption customizes a PaperVenue. Invalid financial configuration panics
// in NewPaperVenue because it is a programmer/startup configuration error.
type PaperOption func(*PaperVenue)

func WithInitialQuote(value contracts.Decimal) PaperOption {
	return func(venue *PaperVenue) { venue.freeQuote = value }
}

func WithQuoteCurrency(value string) PaperOption {
	return func(venue *PaperVenue) {
		if value = strings.TrimSpace(value); value != "" {
			venue.quoteCCY = value
		}
	}
}

func WithSlippageBPS(value contracts.Decimal) PaperOption {
	return func(venue *PaperVenue) { venue.slippageBPS = value }
}

func WithFeeRate(value contracts.Decimal) PaperOption {
	return func(venue *PaperVenue) { venue.feeRate = value }
}

// PaperVenue is a concurrency-safe deterministic in-memory venue. Monetary
// arithmetic uses contracts.Decimal end to end; float64 is used only for the
// wire-compatible leverage field and is converted through its decimal text.
type PaperVenue struct {
	mu sync.RWMutex

	quoteCCY    string
	freeQuote   contracts.Decimal
	slippageBPS contracts.Decimal
	feeRate     contracts.Decimal

	marks      map[string]contracts.Decimal
	positions  map[positionKey]contracts.Position
	protective map[positionKey]contracts.List[contracts.ProtectiveOrder]
	fills      map[string]contracts.ExecutionResult
	sequence   uint64
}

var _ Venue = (*PaperVenue)(nil)

// NewPaperVenue creates the safe zero-key default venue.
func NewPaperVenue(options ...PaperOption) *PaperVenue {
	venue := &PaperVenue{
		quoteCCY:    "USDT",
		freeQuote:   contracts.MustDecimal("10000"),
		slippageBPS: contracts.MustDecimal("5"),
		feeRate:     contracts.MustDecimal("0.0004"),
		marks:       make(map[string]contracts.Decimal),
		positions:   make(map[positionKey]contracts.Position),
		protective:  make(map[positionKey]contracts.List[contracts.ProtectiveOrder]),
		fills:       make(map[string]contracts.ExecutionResult),
	}
	for _, option := range options {
		option(venue)
	}
	if venue.freeQuote.IsNegative() {
		panic("paper venue initial quote cannot be negative")
	}
	if venue.slippageBPS.IsNegative() {
		panic("paper venue slippage cannot be negative")
	}
	if venue.feeRate.IsNegative() {
		panic("paper venue fee rate cannot be negative")
	}
	return venue
}

func (*PaperVenue) ID() string         { return "paper" }
func (*PaperVenue) Kind() Kind         { return KindPaper }
func (*PaperVenue) IsConfigured() bool { return true }
func (venue *PaperVenue) ExecutionIdentity() ExecutionIdentity {
	return ExecutionIdentity{
		VenueID: venue.ID(), Kind: venue.Kind(), Environment: EnvironmentPaper, Writable: true,
	}
}
func (*PaperVenue) Caps() Caps {
	return Caps{Spot: true, Perp: true, NativeProtectiveOrders: true, ReadOnly: false}
}

// SetMarkPrice updates a symbol and synchronously applies stop-loss/take-profit
// triggers. Trigger fills are idempotently recorded before this method returns.
func (v *PaperVenue) SetMarkPrice(symbol string, price contracts.Decimal) error {
	if strings.TrimSpace(symbol) == "" || !price.IsPositive() {
		return ErrInvalidMarkPrice
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.marks[symbol] = price
	v.triggerProtectiveLocked(symbol, price)
	return nil
}

func (v *PaperVenue) FetchTicker(ctx context.Context, symbol string) (contracts.Decimal, error) {
	if err := contextError(ctx); err != nil {
		return contracts.Zero(), err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	price, ok := v.marks[symbol]
	if !ok {
		return contracts.Zero(), fmt.Errorf("%w: %s", ErrMarkPriceUnavailable, symbol)
	}
	return price, nil
}

func (*PaperVenue) FetchOHLCV(
	ctx context.Context,
	_ string,
	_ string,
	_ int,
) ([]contracts.Candle, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return []contracts.Candle{}, nil
}

func (*PaperVenue) FetchOrderBook(
	ctx context.Context,
	_ string,
	_ int,
) (contracts.OrderBook, error) {
	if err := contextError(ctx); err != nil {
		return contracts.OrderBook{}, err
	}
	return contracts.OrderBook{}, nil
}

func (v *PaperVenue) Positions(ctx context.Context) ([]contracts.Position, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	positions := make([]contracts.Position, 0, len(v.positions))
	for _, position := range v.positions {
		positions = append(positions, clonePosition(position))
	}
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Symbol == positions[j].Symbol {
			return positions[i].Instrument < positions[j].Instrument
		}
		return positions[i].Symbol < positions[j].Symbol
	})
	return positions, nil
}

// ProtectiveFor returns independent copies of all protective orders for a
// symbol, across spot and perpetual positions.
func (v *PaperVenue) ProtectiveFor(symbol string) []contracts.ProtectiveOrder {
	v.mu.RLock()
	defer v.mu.RUnlock()
	orders := make([]contracts.ProtectiveOrder, 0)
	for key, current := range v.protective {
		if key.symbol == symbol {
			orders = append(orders, current...)
		}
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].OrderID < orders[j].OrderID })
	return orders
}

func (v *PaperVenue) ProtectiveOrders(ctx context.Context, symbol string) ([]contracts.ProtectiveOrder, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return v.ProtectiveFor(symbol), nil
}

func (v *PaperVenue) CancelProtectiveOrders(ctx context.Context, symbol string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for key := range v.protective {
		if key.symbol == symbol {
			delete(v.protective, key)
		}
	}
	return nil
}

func (v *PaperVenue) Balances(ctx context.Context) (contracts.Balances, error) {
	if err := contextError(ctx); err != nil {
		return contracts.Balances{}, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	equity := v.freeQuote
	for _, position := range v.positions {
		mark, ok := v.marks[position.Symbol]
		if !ok {
			mark = position.EntryPrice
		}
		unrealized := position.SizeBase.Mul(mark.Sub(position.EntryPrice))
		if position.Side == contracts.SideShort {
			unrealized = unrealized.Neg()
		}
		entryNotional := position.SizeBase.Mul(position.EntryPrice)
		if position.Instrument == contracts.InstrumentPerp {
			leverage, err := leverageDecimal(position.Leverage)
			if err != nil {
				return contracts.Balances{}, err
			}
			margin, err := entryNotional.Quo(leverage)
			if err != nil {
				return contracts.Balances{}, err
			}
			equity = equity.Add(margin).Add(unrealized)
		} else {
			equity = equity.Add(entryNotional).Add(unrealized)
		}
	}
	return contracts.Balances{
		QuoteCCY:   v.quoteCCY,
		FreeQuote:  v.freeQuote,
		TotalQuote: equity,
	}, nil
}

func (v *PaperVenue) Preflight(
	ctx context.Context,
	intent contracts.OrderIntent,
) (PreflightReport, error) {
	if err := contextError(ctx); err != nil {
		return PreflightReport{}, err
	}
	intent = normalizeIntent(intent)
	v.mu.RLock()
	defer v.mu.RUnlock()
	key := positionKey{symbol: intent.Symbol, instrument: intent.Instrument}
	if (intent.ReduceOnly || intent.Side == contracts.SideFlat) && v.positions[key].Side != "" {
		intent.Side = v.positions[key].Side
	}
	return v.preflightLocked(intent)
}

func (v *PaperVenue) Place(
	ctx context.Context,
	intent contracts.OrderIntent,
) (contracts.ExecutionResult, error) {
	if err := contextError(ctx); err != nil {
		return contracts.ExecutionResult{}, err
	}
	if strings.TrimSpace(intent.ClientID) == "" {
		return contracts.ExecutionResult{}, ErrClientIDRequired
	}
	intent = normalizeIntent(intent)
	v.mu.Lock()
	defer v.mu.Unlock()
	if existing, ok := v.fills[intent.ClientID]; ok {
		return cloneExecution(existing), nil
	}

	key := positionKey{symbol: intent.Symbol, instrument: intent.Instrument}
	if (intent.ReduceOnly || intent.Side == contracts.SideFlat) && v.positions[key].Side != "" {
		intent.Side = v.positions[key].Side
	}
	preflight, err := v.preflightLocked(intent)
	if err != nil {
		return contracts.ExecutionResult{}, err
	}
	if !preflight.OK {
		return v.rememberRejectedLocked(intent.ClientID, strings.Join(preflight.Reasons, "; ")), nil
	}
	if intent.ReduceOnly || intent.Side == contracts.SideFlat {
		result := v.closeAtPriceLocked(key, preflight.EstPrice, intent.ClientID, "")
		v.fills[intent.ClientID] = cloneExecution(result)
		return cloneExecution(result), nil
	}
	if intent.Side != contracts.SideLong && intent.Side != contracts.SideShort {
		return v.rememberRejectedLocked(intent.ClientID, "无效的开仓方向"), nil
	}
	if intent.Instrument != contracts.InstrumentSpot && intent.Instrument != contracts.InstrumentPerp {
		return v.rememberRejectedLocked(intent.ClientID, "无效的交易品种"), nil
	}
	if intent.SizeQuote.IsZero() || intent.SizeQuote.IsNegative() {
		return v.rememberRejectedLocked(intent.ClientID, "开仓金额必须大于 0"), nil
	}
	leverage, err := leverageDecimal(intent.Leverage)
	if err != nil {
		return v.rememberRejectedLocked(intent.ClientID, err.Error()), nil
	}
	cost := intent.SizeQuote
	if intent.Instrument == contracts.InstrumentPerp {
		cost, err = intent.SizeQuote.Quo(leverage)
		if err != nil {
			return contracts.ExecutionResult{}, err
		}
	}
	existing, adding := v.positions[key]
	if adding {
		if existing.Side != intent.Side {
			return v.rememberRejectedLocked(intent.ClientID, "该标的已有反向持仓"), nil
		}
		if intent.Instrument == contracts.InstrumentPerp {
			existingLeverage, leverageErr := leverageDecimal(existing.Leverage)
			if leverageErr != nil {
				return v.rememberRejectedLocked(intent.ClientID, leverageErr.Error()), nil
			}
			existingNotional := existing.SizeBase.Mul(existing.EntryPrice)
			existingMargin, marginErr := existingNotional.Quo(existingLeverage)
			if marginErr != nil {
				return contracts.ExecutionResult{}, marginErr
			}
			newTotalMargin, marginErr := existingNotional.Add(intent.SizeQuote).Quo(leverage)
			if marginErr != nil {
				return contracts.ExecutionResult{}, marginErr
			}
			cost = newTotalMargin.Sub(existingMargin)
		}
	}
	fee := intent.SizeQuote.Mul(v.feeRate)
	required := cost.Add(fee)
	if required.IsPositive() && v.freeQuote.Cmp(required) < 0 {
		message := fmt.Sprintf("可用余额不足：需要 %s，当前 %s", required.String(), v.freeQuote.String())
		return v.rememberRejectedLocked(intent.ClientID, message), nil
	}
	sizeBase, err := intent.SizeQuote.Quo(preflight.EstPrice)
	if err != nil {
		return contracts.ExecutionResult{}, err
	}

	position := contracts.Position{
		Symbol:     intent.Symbol,
		Venue:      v.ID(),
		Side:       intent.Side,
		Instrument: intent.Instrument,
		SizeBase:   sizeBase,
		EntryPrice: preflight.EstPrice,
		Leverage:   intent.Leverage,
	}
	if preflight.EstLiquidationPrice != nil {
		position.LiqPrice = decimalPointer(*preflight.EstLiquidationPrice)
	}
	if intent.Instrument == contracts.InstrumentPerp {
		position.MarginMode = marginModePointer(intent.MarginMode)
	}
	protective := v.makeProtectiveLocked(intent)
	if adding {
		totalBase := existing.SizeBase.Add(sizeBase)
		totalEntryNotional := existing.SizeBase.Mul(existing.EntryPrice).Add(sizeBase.Mul(preflight.EstPrice))
		averageEntry, averageErr := totalEntryNotional.Quo(totalBase)
		if averageErr != nil {
			return contracts.ExecutionResult{}, averageErr
		}
		position = existing
		position.SizeBase = totalBase
		position.EntryPrice = averageEntry
		position.Leverage = intent.Leverage
		if intent.Instrument == contracts.InstrumentPerp {
			position.MarginMode = marginModePointer(intent.MarginMode)
			inverse, inverseErr := decimalOne.Quo(leverage)
			if inverseErr != nil {
				return contracts.ExecutionResult{}, inverseErr
			}
			liquidation := averageEntry.Mul(decimalOne.Add(inverse))
			if intent.Side == contracts.SideLong {
				liquidation = averageEntry.Mul(decimalOne.Sub(inverse))
			}
			position.LiqPrice = decimalPointer(liquidation)
		}
		protective = conservativeProtectiveOrders(existing.Side, v.protective[key], protective)
	}
	orderID := v.nextIDLocked("ord")
	result := contracts.ExecutionResult{
		ClientID:         intent.ClientID,
		OrderID:          stringPointer(orderID),
		Status:           contracts.OrderStatusFilled,
		FilledBase:       sizeBase,
		AvgPrice:         decimalPointer(preflight.EstPrice),
		FeeQuote:         fee,
		SlippageBPS:      decimalPointer(v.slippageBPS),
		ProtectiveOrders: append(contracts.List[contracts.ProtectiveOrder]{}, protective...),
	}
	v.freeQuote = v.freeQuote.Sub(required)
	v.positions[key] = position
	v.protective[key] = protective
	v.fills[intent.ClientID] = cloneExecution(result)
	return cloneExecution(result), nil
}

func conservativeProtectiveOrders(
	side contracts.Side,
	existing contracts.List[contracts.ProtectiveOrder],
	added contracts.List[contracts.ProtectiveOrder],
) contracts.List[contracts.ProtectiveOrder] {
	all := append(append(contracts.List[contracts.ProtectiveOrder]{}, existing...), added...)
	var stop, take *contracts.ProtectiveOrder
	for index := range all {
		order := all[index]
		switch order.Kind {
		case "stop_loss":
			if stop == nil || side == contracts.SideLong && order.TriggerPrice.Cmp(stop.TriggerPrice) > 0 ||
				side == contracts.SideShort && order.TriggerPrice.Cmp(stop.TriggerPrice) < 0 {
				copy := order
				stop = &copy
			}
		case "take_profit":
			if take == nil || side == contracts.SideLong && order.TriggerPrice.Cmp(take.TriggerPrice) < 0 ||
				side == contracts.SideShort && order.TriggerPrice.Cmp(take.TriggerPrice) > 0 {
				copy := order
				take = &copy
			}
		}
	}
	result := contracts.List[contracts.ProtectiveOrder]{}
	if stop != nil {
		result = append(result, *stop)
	}
	if take != nil {
		result = append(result, *take)
	}
	return result
}

// Fill returns an immutable snapshot of a prior idempotent result, including
// synthetic fills created by protective-order triggers.
func (v *PaperVenue) Fill(clientID string) (contracts.ExecutionResult, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	result, ok := v.fills[clientID]
	return cloneExecution(result), ok
}

// Cancel is intentionally a no-op for paper execution, matching the public
// venue: protective orders are modeled as attached position guards rather than
// independently cancelable exchange orders.
func (*PaperVenue) Cancel(ctx context.Context, _ string) error {
	return contextError(ctx)
}

// Close satisfies lifecycle cleanup without discarding inspectable test state.
func (*PaperVenue) Close() error { return nil }

func (v *PaperVenue) preflightLocked(intent contracts.OrderIntent) (PreflightReport, error) {
	mark, ok := v.marks[intent.Symbol]
	if !ok || !mark.IsPositive() {
		return PreflightReport{
			OK:       false,
			EstPrice: contracts.Zero(),
			Reasons:  contracts.List[string]{"无 mark price"},
		}, nil
	}
	slip, err := v.slippageBPS.Quo(decimalTenThousand)
	if err != nil {
		return PreflightReport{}, err
	}
	adverseUp := (intent.Side == contracts.SideLong && !intent.ReduceOnly) ||
		(intent.Side == contracts.SideShort && intent.ReduceOnly)
	estPrice := mark.Mul(decimalOne.Sub(slip))
	if adverseUp {
		estPrice = mark.Mul(decimalOne.Add(slip))
	}
	report := PreflightReport{
		OK:             true,
		EstPrice:       estPrice,
		EstSlippageBPS: decimalPointer(v.slippageBPS),
	}
	if intent.Instrument == contracts.InstrumentPerp {
		leverage, leverageErr := leverageDecimal(intent.Leverage)
		if leverageErr != nil {
			return PreflightReport{
				OK:       false,
				EstPrice: estPrice,
				Reasons:  contracts.List[string]{leverageErr.Error()},
			}, nil
		}
		inverse, divideErr := decimalOne.Quo(leverage)
		if divideErr != nil {
			return PreflightReport{}, divideErr
		}
		liquidation := estPrice.Mul(decimalOne.Add(inverse))
		if intent.Side == contracts.SideLong {
			liquidation = estPrice.Mul(decimalOne.Sub(inverse))
		}
		report.EstLiquidationPrice = decimalPointer(liquidation)
	}
	return report, nil
}

func (v *PaperVenue) rememberRejectedLocked(clientID, message string) contracts.ExecutionResult {
	result := contracts.ExecutionResult{
		ClientID: clientID,
		Status:   contracts.OrderStatusRejected,
		Error:    stringPointer(message),
	}
	v.fills[clientID] = cloneExecution(result)
	return cloneExecution(result)
}

func (v *PaperVenue) closeAtPriceLocked(
	key positionKey,
	price contracts.Decimal,
	clientID string,
	orderID string,
) contracts.ExecutionResult {
	position, ok := v.positions[key]
	if !ok {
		return contracts.ExecutionResult{
			ClientID: clientID,
			Status:   contracts.OrderStatusRejected,
			Error:    stringPointer("无持仓可平"),
		}
	}
	delete(v.positions, key)
	delete(v.protective, key)
	closeNotional := position.SizeBase.Mul(price)
	fee := closeNotional.Mul(v.feeRate)
	pnl := position.SizeBase.Mul(price.Sub(position.EntryPrice))
	if position.Side == contracts.SideShort {
		pnl = pnl.Neg()
	}
	entryNotional := position.SizeBase.Mul(position.EntryPrice)
	proceeds := entryNotional.Add(pnl)
	if position.Instrument == contracts.InstrumentPerp {
		leverage, err := leverageDecimal(position.Leverage)
		if err == nil {
			if margin, divideErr := entryNotional.Quo(leverage); divideErr == nil {
				proceeds = margin.Add(pnl)
			}
		}
	}
	v.freeQuote = v.freeQuote.Add(proceeds).Sub(fee)
	if orderID == "" {
		orderID = v.nextIDLocked("close")
	}
	return contracts.ExecutionResult{
		ClientID:    clientID,
		OrderID:     stringPointer(orderID),
		Status:      contracts.OrderStatusFilled,
		FilledBase:  position.SizeBase,
		AvgPrice:    decimalPointer(price),
		FeeQuote:    fee,
		SlippageBPS: decimalPointer(v.slippageBPS),
	}
}

func (v *PaperVenue) makeProtectiveLocked(
	intent contracts.OrderIntent,
) contracts.List[contracts.ProtectiveOrder] {
	orders := make(contracts.List[contracts.ProtectiveOrder], 0, 1+len(intent.TakeProfit))
	if intent.StopLoss != nil {
		orders = append(orders, contracts.ProtectiveOrder{
			Kind:         "stop_loss",
			OrderID:      v.nextIDLocked("sl"),
			TriggerPrice: *intent.StopLoss,
			ReduceOnly:   true,
		})
	}
	for _, price := range intent.TakeProfit {
		orders = append(orders, contracts.ProtectiveOrder{
			Kind:         "take_profit",
			OrderID:      v.nextIDLocked("tp"),
			TriggerPrice: price,
			ReduceOnly:   true,
		})
	}
	return orders
}

func (v *PaperVenue) triggerProtectiveLocked(symbol string, mark contracts.Decimal) {
	keys := make([]positionKey, 0)
	for key := range v.positions {
		if key.symbol == symbol {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].instrument < keys[j].instrument })
	for _, key := range keys {
		position, exists := v.positions[key]
		if !exists {
			continue
		}
		for _, order := range v.protective[key] {
			if !protectiveHit(position, order, mark) {
				continue
			}
			clientID := order.OrderID + "-trigger"
			if _, duplicate := v.fills[clientID]; !duplicate {
				result := v.closeAtPriceLocked(key, mark, clientID, order.OrderID)
				v.fills[clientID] = cloneExecution(result)
			}
			break
		}
	}
}

func protectiveHit(
	position contracts.Position,
	order contracts.ProtectiveOrder,
	mark contracts.Decimal,
) bool {
	comparison := mark.Cmp(order.TriggerPrice)
	if position.Side == contracts.SideLong {
		if order.Kind == "stop_loss" {
			return comparison <= 0
		}
		return comparison >= 0
	}
	if order.Kind == "stop_loss" {
		return comparison >= 0
	}
	return comparison <= 0
}

func (v *PaperVenue) nextIDLocked(prefix string) string {
	v.sequence++
	return fmt.Sprintf("paper-%s-%d", prefix, v.sequence)
}

func normalizeIntent(intent contracts.OrderIntent) contracts.OrderIntent {
	if intent.Instrument == "" {
		intent.Instrument = contracts.InstrumentSpot
	}
	if intent.OrderType == "" {
		intent.OrderType = contracts.EntryTypeMarket
	}
	if intent.Leverage == 0 {
		intent.Leverage = 1
	}
	if intent.MarginMode == "" {
		intent.MarginMode = contracts.MarginModeIsolated
	}
	if intent.Venue == "" {
		intent.Venue = "paper"
	}
	return intent
}

func leverageDecimal(leverage float64) (contracts.Decimal, error) {
	if math.IsNaN(leverage) || math.IsInf(leverage, 0) || leverage < 1 {
		return contracts.Zero(), errors.New("杠杆必须是大于等于 1 的有限数")
	}
	return contracts.ParseDecimal(strconv.FormatFloat(leverage, 'g', -1, 64))
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func clonePosition(position contracts.Position) contracts.Position {
	if position.LiqPrice != nil {
		position.LiqPrice = decimalPointer(*position.LiqPrice)
	}
	if position.MarginMode != nil {
		position.MarginMode = marginModePointer(*position.MarginMode)
	}
	if position.Chain != nil {
		position.Chain = stringPointer(*position.Chain)
	}
	if position.TxHash != nil {
		position.TxHash = stringPointer(*position.TxHash)
	}
	return position
}

func cloneExecution(result contracts.ExecutionResult) contracts.ExecutionResult {
	if result.OrderID != nil {
		result.OrderID = stringPointer(*result.OrderID)
	}
	if result.AvgPrice != nil {
		result.AvgPrice = decimalPointer(*result.AvgPrice)
	}
	if result.SlippageBPS != nil {
		result.SlippageBPS = decimalPointer(*result.SlippageBPS)
	}
	if result.Error != nil {
		result.Error = stringPointer(*result.Error)
	}
	result.ProtectiveOrders = append(contracts.List[contracts.ProtectiveOrder]{}, result.ProtectiveOrders...)
	return result
}

func decimalPointer(value contracts.Decimal) *contracts.Decimal { return &value }
func stringPointer(value string) *string                        { return &value }
func marginModePointer(value contracts.MarginMode) *contracts.MarginMode {
	return &value
}
