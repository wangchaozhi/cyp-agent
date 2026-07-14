package venue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

const (
	okxOrderPollAttempts = 12
	okxSettlementTimeout = 15 * time.Second
)

type okxInstrumentSpec struct {
	InstrumentID          string
	BaseCurrency          string
	QuoteCurrency         string
	SettlementCurrency    string
	ContractType          string
	ContractValueCurrency string
	ContractValue         contracts.Decimal
	LotSize               contracts.Decimal
	MinSize               contracts.Decimal
	TickSize              contracts.Decimal
}

type okxOrderRef struct {
	InstrumentID string
	OrderID      string
}

type okxOrderAck struct {
	OrderID       string `json:"ordId"`
	ClientOrderID string `json:"clOrdId"`
	Code          string `json:"sCode"`
	Message       string `json:"sMsg"`
}

type okxOrderAckEnvelope struct {
	Code string        `json:"code"`
	Data []okxOrderAck `json:"data"`
}

type okxOrderDetail struct {
	OrderID       string `json:"ordId"`
	ClientOrderID string `json:"clOrdId"`
	State         string `json:"state"`
	AveragePrice  any    `json:"avgPx"`
	FilledSize    any    `json:"accFillSz"`
	Fee           any    `json:"fee"`
	FeeCurrency   string `json:"feeCcy"`
}

type okxOrderDetailEnvelope struct {
	Code string           `json:"code"`
	Data []okxOrderDetail `json:"data"`
}

// ReconcileOrder authoritatively looks up an OKX order (Demo or explicitly
// enabled live) by the same deterministic clOrdId used during submission. It
// never retries Place.
func (venue *CEXVenue) ReconcileOrder(ctx context.Context, intent contracts.OrderIntent) (contracts.ExecutionResult, bool, error) {
	if ctx == nil {
		return contracts.ExecutionResult{}, false, errors.New("OKX order reconcile context is required")
	}
	if !venue.TradingEnabled() || venue.id != "okx" {
		return contracts.ExecutionResult{}, false, ErrCEXTradingDisabled
	}
	instrumentID := okxInstrumentID(intent.Symbol)
	spec, err := venue.okxInstrument(ctx, instrumentID)
	if err != nil {
		return contracts.ExecutionResult{}, false, err
	}
	var referencePrice contracts.Decimal
	if intent.Price != nil && intent.Price.IsPositive() {
		referencePrice = *intent.Price
	} else {
		referencePrice, err = venue.FetchTicker(ctx, intent.Symbol)
		if err != nil {
			return contracts.ExecutionResult{}, false, err
		}
	}
	result, reference, found, err := venue.recoverOKXOrder(
		ctx, intent, referencePrice, spec, instrumentID,
		SanitizeOKXClientID(intent.ClientID, ""), okxProtectiveOrders(intent), false,
	)
	if err != nil || !found {
		return contracts.ExecutionResult{}, found, err
	}
	venue.rememberCEXResult(intent.ClientID, result, reference)
	return cloneExecution(result), true, nil
}

var _ OrderReconciler = (*CEXVenue)(nil)

func (venue *CEXVenue) placeOKX(
	ctx context.Context,
	intent contracts.OrderIntent,
) (contracts.ExecutionResult, okxOrderRef, error) {
	if ctx == nil {
		return contracts.ExecutionResult{}, okxOrderRef{}, &CEXError{
			Kind: CEXErrorValidation, Exchange: venue.id, Operation: "place", Message: "context is required",
		}
	}
	if !strings.EqualFold(strings.TrimSpace(intent.Venue), "okx") {
		return rejectedOKX(intent.ClientID, "订单场所与 OKX 执行器不匹配"), okxOrderRef{}, nil
	}
	if intent.Instrument != contracts.InstrumentPerp ||
		!strings.HasSuffix(strings.ToUpper(strings.TrimSpace(intent.Symbol)), ":USDT") {
		return rejectedOKX(intent.ClientID, "OKX 当前仅开放 USDT 永续合约执行"), okxOrderRef{}, nil
	}
	if intent.Side != contracts.SideLong && intent.Side != contracts.SideShort {
		return rejectedOKX(intent.ClientID, "OKX 下单方向必须为 long 或 short"), okxOrderRef{}, nil
	}
	if !intent.SizeQuote.IsPositive() {
		return rejectedOKX(intent.ClientID, "OKX 下单金额必须大于 0"), okxOrderRef{}, nil
	}

	instrumentID := okxInstrumentID(intent.Symbol)
	spec, err := venue.okxInstrument(ctx, instrumentID)
	if err != nil {
		return contracts.ExecutionResult{}, okxOrderRef{}, err
	}
	referencePrice, err := venue.FetchTicker(ctx, intent.Symbol)
	if err != nil {
		return contracts.ExecutionResult{}, okxOrderRef{}, err
	}
	sizingPrice := referencePrice
	usesSpecifiedPrice := intent.ReduceOnly || intent.OrderType == contracts.EntryTypeLimit ||
		intent.OrderType == contracts.EntryTypeRange
	if usesSpecifiedPrice && intent.Price != nil && intent.Price.IsPositive() {
		sizingPrice = *intent.Price
	}
	orderSize, err := okxContractOrderSize(intent.SizeQuote, sizingPrice, spec)
	if err != nil {
		return rejectedOKX(intent.ClientID, err.Error()), okxOrderRef{}, nil
	}
	positionMode, err := venue.okxPositionMode(ctx)
	if err != nil {
		return contracts.ExecutionResult{}, okxOrderRef{}, err
	}
	side, positionSide := okxOrderDirection(intent, positionMode)
	if !intent.ReduceOnly {
		if err := venue.setOKXLeverage(ctx, intent, instrumentID, positionMode, positionSide); err != nil {
			return contracts.ExecutionResult{}, okxOrderRef{}, err
		}
	}

	clientOrderID := SanitizeOKXClientID(intent.ClientID, "")
	orderType := "market"
	body := map[string]any{
		"instId":  instrumentID,
		"tdMode":  string(intent.MarginMode),
		"clOrdId": clientOrderID,
		"side":    side,
		"ordType": orderType,
		"sz":      orderSize.String(),
	}
	if positionMode == "long_short_mode" {
		body["posSide"] = positionSide
	}
	if intent.ReduceOnly && positionMode != "long_short_mode" {
		body["reduceOnly"] = true
	}
	if intent.OrderType == contracts.EntryTypeLimit || intent.OrderType == contracts.EntryTypeRange {
		price := referencePrice
		if intent.Price != nil && intent.Price.IsPositive() {
			price = *intent.Price
		}
		price, err = QuantizeDown(price, spec.TickSize)
		if err != nil || !price.IsPositive() {
			return rejectedOKX(intent.ClientID, "OKX 限价无效"), okxOrderRef{}, nil
		}
		orderType = "limit"
		body["ordType"] = orderType
		body["px"] = price.String()
	}
	protective := okxProtectiveOrders(intent)
	if !intent.ReduceOnly {
		if attached := okxAttachedProtection(intent); len(attached) > 0 {
			body["attachAlgoOrds"] = attached
		}
	}

	var accepted okxOrderAckEnvelope
	if err := venue.okxPrivatePOST(ctx, "/api/v5/trade/order", body, &accepted); err != nil {
		// Never retry an order POST blindly: the exchange may have accepted it
		// before the response was lost. Reconcile by the deterministic clOrdId
		// first so a transport timeout cannot become a duplicate order.
		if isAmbiguousOKXSubmission(err) {
			recoveryContext, cancelRecovery := context.WithTimeout(context.WithoutCancel(ctx), okxSettlementTimeout)
			defer cancelRecovery()
			result, reference, recovered, recoveryErr := venue.recoverOKXOrder(
				recoveryContext, intent, referencePrice, spec, instrumentID, clientOrderID, protective, true,
			)
			if recovered {
				return result, reference, recoveryErr
			}
			if recoveryErr != nil {
				return contracts.ExecutionResult{}, okxOrderRef{}, errors.Join(ErrOrderStateUnknown, err,
					fmt.Errorf("reconcile ambiguous OKX order: %w", recoveryErr))
			}
			return contracts.ExecutionResult{}, okxOrderRef{}, errors.Join(ErrOrderStateUnknown, err)
		}
		return contracts.ExecutionResult{}, okxOrderRef{}, err
	}
	if len(accepted.Data) == 0 {
		decodeErr := &CEXError{
			Kind: CEXErrorDecode, Exchange: venue.id, Operation: "place", Message: "empty OKX order acknowledgement",
		}
		return venue.recoverMalformedOKXAcknowledgement(
			ctx, intent, referencePrice, spec, instrumentID, clientOrderID, protective, decodeErr,
		)
	}
	ack := accepted.Data[0]
	if ack.Code != "" && ack.Code != "0" {
		message := strings.TrimSpace(ack.Message)
		if message == "" {
			message = "OKX 拒绝订单"
		}
		return rejectedOKX(intent.ClientID, message), okxOrderRef{}, nil
	}
	if ack.OrderID == "" {
		decodeErr := &CEXError{
			Kind: CEXErrorDecode, Exchange: venue.id, Operation: "place", Message: "OKX acknowledgement omitted ordId",
		}
		return venue.recoverMalformedOKXAcknowledgement(
			ctx, intent, referencePrice, spec, instrumentID, clientOrderID, protective, decodeErr,
		)
	}
	reference := okxOrderRef{InstrumentID: instrumentID, OrderID: ack.OrderID}
	// Once OKX acknowledged the order, caller cancellation must not stop
	// authoritative settlement. Continue on a bounded detached context so the
	// process either observes a terminal state or cancels and confirms it.
	settlementContext, cancelSettlement := context.WithTimeout(context.WithoutCancel(ctx), okxSettlementTimeout)
	defer cancelSettlement()
	result, err := venue.waitOKXOrder(settlementContext, intent, referencePrice, spec, reference, protective, true)
	if err != nil {
		// The exchange already acknowledged this order. Any later read/cancel
		// failure is an unknown remote state and must freeze execution until
		// startup reconciliation proves the outcome.
		return contracts.ExecutionResult{}, reference, errors.Join(ErrOrderStateUnknown, err)
	}
	return result, reference, nil
}

func (venue *CEXVenue) recoverMalformedOKXAcknowledgement(
	ctx context.Context,
	intent contracts.OrderIntent,
	referencePrice contracts.Decimal,
	spec okxInstrumentSpec,
	instrumentID, clientOrderID string,
	protective contracts.List[contracts.ProtectiveOrder],
	acknowledgementErr error,
) (contracts.ExecutionResult, okxOrderRef, error) {
	recoveryContext, cancelRecovery := context.WithTimeout(context.WithoutCancel(ctx), okxSettlementTimeout)
	defer cancelRecovery()
	result, reference, recovered, recoveryErr := venue.recoverOKXOrder(
		recoveryContext, intent, referencePrice, spec, instrumentID, clientOrderID, protective, true,
	)
	if recovered {
		return result, reference, recoveryErr
	}
	if recoveryErr != nil {
		return contracts.ExecutionResult{}, okxOrderRef{}, errors.Join(
			ErrOrderStateUnknown, acknowledgementErr,
			fmt.Errorf("reconcile malformed OKX acknowledgement: %w", recoveryErr),
		)
	}
	return contracts.ExecutionResult{}, okxOrderRef{}, errors.Join(ErrOrderStateUnknown, acknowledgementErr)
}

func isAmbiguousOKXSubmission(err error) bool {
	var classified *CEXError
	return errors.As(err, &classified) && classified.Retryable()
}

func (venue *CEXVenue) recoverOKXOrder(
	ctx context.Context,
	intent contracts.OrderIntent,
	referencePrice contracts.Decimal,
	spec okxInstrumentSpec,
	instrumentID, clientOrderID string,
	protective contracts.List[contracts.ProtectiveOrder],
	cancelActive bool,
) (contracts.ExecutionResult, okxOrderRef, bool, error) {
	var payload okxOrderDetailEnvelope
	err := venue.doJSON(ctx, http.MethodGet, "/api/v5/trade/order", url.Values{
		"instId": {instrumentID}, "clOrdId": {clientOrderID},
	}, true, &payload)
	if err != nil {
		return contracts.ExecutionResult{}, okxOrderRef{}, false, err
	}
	if len(payload.Data) == 0 || strings.TrimSpace(payload.Data[0].OrderID) == "" {
		return contracts.ExecutionResult{}, okxOrderRef{}, false, nil
	}
	reference := okxOrderRef{InstrumentID: instrumentID, OrderID: payload.Data[0].OrderID}
	result, err := venue.waitOKXOrder(ctx, intent, referencePrice, spec, reference, protective, cancelActive)
	return result, reference, true, err
}

func (venue *CEXVenue) okxInstrument(ctx context.Context, instrumentID string) (okxInstrumentSpec, error) {
	venue.mu.RLock()
	cached, ok := venue.instruments[instrumentID]
	venue.mu.RUnlock()
	if ok {
		return cached, nil
	}
	var payload struct {
		Code string `json:"code"`
		Data []struct {
			InstrumentID          string `json:"instId"`
			BaseCurrency          string `json:"baseCcy"`
			QuoteCurrency         string `json:"quoteCcy"`
			SettlementCurrency    string `json:"settleCcy"`
			ContractType          string `json:"ctType"`
			ContractValueCurrency string `json:"ctValCcy"`
			ContractValue         any    `json:"ctVal"`
			LotSize               any    `json:"lotSz"`
			MinSize               any    `json:"minSz"`
			TickSize              any    `json:"tickSz"`
			State                 string `json:"state"`
		} `json:"data"`
	}
	if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/public/instruments", url.Values{
		"instType": {"SWAP"}, "instId": {instrumentID},
	}, false, &payload); err != nil {
		return okxInstrumentSpec{}, err
	}
	if len(payload.Data) == 0 || payload.Data[0].State != "live" {
		return okxInstrumentSpec{}, &CEXError{
			Kind: CEXErrorValidation, Exchange: venue.id, Operation: "instrument", Message: "OKX instrument is unavailable",
		}
	}
	row := payload.Data[0]
	baseCurrency, quoteCurrency := row.BaseCurrency, row.QuoteCurrency
	if baseCurrency == "" || quoteCurrency == "" {
		parts := strings.Split(strings.ToUpper(row.InstrumentID), "-")
		if len(parts) >= 3 {
			baseCurrency = parts[0]
			quoteCurrency = parts[1]
		}
	}
	contractValue, err := decimalFromAny(row.ContractValue)
	if err != nil || !contractValue.IsPositive() {
		return okxInstrumentSpec{}, &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "instrument", Message: "invalid ctVal", Err: err}
	}
	lotSize, err := decimalFromAny(row.LotSize)
	if err != nil || !lotSize.IsPositive() {
		return okxInstrumentSpec{}, &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "instrument", Message: "invalid lotSz", Err: err}
	}
	minimum, err := decimalFromAny(row.MinSize)
	if err != nil || !minimum.IsPositive() {
		return okxInstrumentSpec{}, &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "instrument", Message: "invalid minSz", Err: err}
	}
	tickSize, err := decimalFromAny(row.TickSize)
	if err != nil || !tickSize.IsPositive() {
		return okxInstrumentSpec{}, &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "instrument", Message: "invalid tickSz", Err: err}
	}
	spec := okxInstrumentSpec{
		InstrumentID: row.InstrumentID, BaseCurrency: baseCurrency, QuoteCurrency: quoteCurrency,
		SettlementCurrency: row.SettlementCurrency, ContractType: row.ContractType,
		ContractValueCurrency: row.ContractValueCurrency, ContractValue: contractValue,
		LotSize: lotSize, MinSize: minimum, TickSize: tickSize,
	}
	if spec.ContractType != "" && !strings.EqualFold(spec.ContractType, "linear") {
		return okxInstrumentSpec{}, &CEXError{
			Kind: CEXErrorUnsupported, Exchange: venue.id, Operation: "instrument",
			Message: "only linear swaps are supported",
		}
	}
	if !strings.EqualFold(spec.ContractValueCurrency, spec.BaseCurrency) {
		return okxInstrumentSpec{}, &CEXError{
			Kind: CEXErrorUnsupported, Exchange: venue.id, Operation: "instrument",
			Message: "only linear swaps with base-currency ctVal are supported",
		}
	}
	if !strings.EqualFold(spec.QuoteCurrency, "USDT") ||
		(spec.SettlementCurrency != "" && !strings.EqualFold(spec.SettlementCurrency, "USDT")) {
		return okxInstrumentSpec{}, &CEXError{
			Kind: CEXErrorUnsupported, Exchange: venue.id, Operation: "instrument",
			Message: "only USDT-settled linear swaps are supported",
		}
	}
	venue.mu.Lock()
	venue.instruments[instrumentID] = spec
	venue.mu.Unlock()
	return spec, nil
}

func okxContractOrderSize(
	quoteSize contracts.Decimal,
	price contracts.Decimal,
	spec okxInstrumentSpec,
) (contracts.Decimal, error) {
	if !quoteSize.IsPositive() || !price.IsPositive() || !spec.ContractValue.IsPositive() {
		return contracts.Zero(), fmt.Errorf("OKX 无法从无效金额或价格换算合约张数")
	}
	baseSize, err := quoteSize.Quo(price)
	if err != nil {
		return contracts.Zero(), err
	}
	contractsSize, err := baseSize.Quo(spec.ContractValue)
	if err != nil {
		return contracts.Zero(), err
	}
	contractsSize, err = QuantizeDown(contractsSize, spec.LotSize)
	if err != nil {
		return contracts.Zero(), err
	}
	if contractsSize.Cmp(spec.MinSize) < 0 {
		return contracts.Zero(), fmt.Errorf("OKX 下单张数 %s 低于最小值 %s", contractsSize.String(), spec.MinSize.String())
	}
	return contractsSize, nil
}

func (venue *CEXVenue) okxPositionMode(ctx context.Context) (string, error) {
	var payload struct {
		Code string `json:"code"`
		Data []struct {
			PositionMode string `json:"posMode"`
		} `json:"data"`
	}
	if err := privateGET(venue, ctx, "/api/v5/account/config", &payload); err != nil {
		return "", err
	}
	if len(payload.Data) > 0 && payload.Data[0].PositionMode == "long_short_mode" {
		return "long_short_mode", nil
	}
	return "net_mode", nil
}

func okxOrderDirection(intent contracts.OrderIntent, positionMode string) (side, positionSide string) {
	positionSide = string(intent.Side)
	if intent.Side == contracts.SideLong {
		side = "buy"
		if intent.ReduceOnly {
			side = "sell"
		}
	} else {
		side = "sell"
		if intent.ReduceOnly {
			side = "buy"
		}
	}
	if positionMode != "long_short_mode" {
		positionSide = ""
	}
	return side, positionSide
}

func (venue *CEXVenue) setOKXLeverage(
	ctx context.Context,
	intent contracts.OrderIntent,
	instrumentID, positionMode, positionSide string,
) error {
	if intent.Leverage < 1 {
		return &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "set_leverage", Message: "leverage must be at least 1"}
	}
	body := map[string]any{
		"instId":  instrumentID,
		"lever":   strconv.FormatFloat(intent.Leverage, 'f', -1, 64),
		"mgnMode": string(intent.MarginMode),
	}
	if positionMode == "long_short_mode" && intent.MarginMode == contracts.MarginModeIsolated {
		body["posSide"] = positionSide
	}
	var payload struct {
		Code string `json:"code"`
		Data []any  `json:"data"`
	}
	return venue.okxPrivatePOST(ctx, "/api/v5/account/set-leverage", body, &payload)
}

func okxAttachedProtection(intent contracts.OrderIntent) []map[string]any {
	attached := map[string]any{}
	if intent.StopLoss != nil && intent.StopLoss.IsPositive() {
		attached["slTriggerPx"] = intent.StopLoss.String()
		attached["slOrdPx"] = "-1"
		attached["slTriggerPxType"] = "mark"
	}
	if len(intent.TakeProfit) > 0 && intent.TakeProfit[0].IsPositive() {
		attached["tpTriggerPx"] = intent.TakeProfit[0].String()
		attached["tpOrdPx"] = "-1"
		attached["tpTriggerPxType"] = "mark"
	}
	if len(attached) == 0 {
		return nil
	}
	attached["attachAlgoClOrdId"] = SanitizeOKXClientID(intent.ClientID, "protect")
	return []map[string]any{attached}
}

func okxProtectiveOrders(intent contracts.OrderIntent) contracts.List[contracts.ProtectiveOrder] {
	result := contracts.List[contracts.ProtectiveOrder]{}
	if intent.StopLoss != nil && intent.StopLoss.IsPositive() {
		result = append(result, contracts.ProtectiveOrder{
			Kind: "stop_loss", OrderID: SanitizeOKXClientID(intent.ClientID, "protect"),
			TriggerPrice: *intent.StopLoss, ReduceOnly: true,
		})
	}
	if len(intent.TakeProfit) > 0 && intent.TakeProfit[0].IsPositive() {
		result = append(result, contracts.ProtectiveOrder{
			Kind: "take_profit", OrderID: SanitizeOKXClientID(intent.ClientID, "protect"),
			TriggerPrice: intent.TakeProfit[0], ReduceOnly: true,
		})
	}
	return result
}

func (venue *CEXVenue) waitOKXOrder(
	ctx context.Context,
	intent contracts.OrderIntent,
	referencePrice contracts.Decimal,
	spec okxInstrumentSpec,
	reference okxOrderRef,
	protective contracts.List[contracts.ProtectiveOrder],
	cancelActive bool,
) (contracts.ExecutionResult, error) {
	var last okxOrderDetail
	for attempt := 0; attempt < okxOrderPollAttempts; attempt++ {
		detail, err := venue.loadOKXOrderDetail(ctx, reference)
		if err != nil {
			return contracts.ExecutionResult{}, err
		}
		last = detail
		if last.State == "filled" || last.State == "canceled" || last.State == "mmp_canceled" {
			break
		}
		if attempt+1 < okxOrderPollAttempts {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return contracts.ExecutionResult{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if cancelActive && last.State != "filled" && last.State != "canceled" && last.State != "mmp_canceled" {
		// A live/partially-filled order may otherwise remain on the book after
		// this method returns. OKX cancel acknowledgement is not authoritative,
		// therefore always poll the order again until a terminal state is seen.
		cancelErr := venue.cancelOKX(ctx, reference)
		terminal, confirmErr := venue.confirmOKXOrderTerminal(ctx, reference)
		if confirmErr != nil {
			return contracts.ExecutionResult{}, errors.Join(ErrOrderStateUnknown, cancelErr, confirmErr)
		}
		last = terminal
		if last.State != "filled" && last.State != "canceled" && last.State != "mmp_canceled" {
			return contracts.ExecutionResult{}, errors.Join(ErrOrderStateUnknown, cancelErr,
				fmt.Errorf("OKX order remained %s after cancellation", last.State))
		}
	}
	result, err := okxExecutionResult(intent, referencePrice, spec, reference, protective, last)
	if err != nil {
		return result, err
	}
	// The attached-algo template only proves what was requested. For a filled
	// entry the exchange is the sole authority on whether protection actually
	// exists, so replace the template with verified pending algos. A failed or
	// empty verification deliberately reports no protection and lets the
	// caller run deterministic remediation instead of trusting the request.
	if result.Status == contracts.OrderStatusFilled && !intent.ReduceOnly && len(protective) > 0 {
		verified, verifyErr := venue.ProtectiveOrders(ctx, intent.Symbol)
		if verifyErr != nil {
			result.ProtectiveOrders = contracts.List[contracts.ProtectiveOrder]{}
		} else {
			result.ProtectiveOrders = append(contracts.List[contracts.ProtectiveOrder]{}, verified...)
		}
	}
	return result, nil
}

func (venue *CEXVenue) loadOKXOrderDetail(ctx context.Context, reference okxOrderRef) (okxOrderDetail, error) {
	var payload okxOrderDetailEnvelope
	if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/trade/order", url.Values{
		"instId": {reference.InstrumentID}, "ordId": {reference.OrderID},
	}, true, &payload); err != nil {
		return okxOrderDetail{}, err
	}
	if len(payload.Data) == 0 {
		return okxOrderDetail{}, &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "order", Message: "empty OKX order detail"}
	}
	return payload.Data[0], nil
}

func (venue *CEXVenue) confirmOKXOrderTerminal(ctx context.Context, reference okxOrderRef) (okxOrderDetail, error) {
	var last okxOrderDetail
	for attempt := 0; attempt < okxOrderPollAttempts; attempt++ {
		detail, err := venue.loadOKXOrderDetail(ctx, reference)
		if err != nil {
			return okxOrderDetail{}, err
		}
		last = detail
		if last.State == "filled" || last.State == "canceled" || last.State == "mmp_canceled" {
			return last, nil
		}
		if attempt+1 < okxOrderPollAttempts {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return okxOrderDetail{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return last, nil
}

func okxExecutionResult(
	intent contracts.OrderIntent,
	referencePrice contracts.Decimal,
	spec okxInstrumentSpec,
	reference okxOrderRef,
	protective contracts.List[contracts.ProtectiveOrder],
	detail okxOrderDetail,
) (contracts.ExecutionResult, error) {
	status := contracts.OrderStatusAcknowledged
	switch detail.State {
	case "filled":
		status = contracts.OrderStatusFilled
	case "partially_filled":
		status = contracts.OrderStatusPartiallyFilled
	case "canceled", "mmp_canceled":
		status = contracts.OrderStatusCanceled
	}
	result := contracts.ExecutionResult{
		ClientID: intent.ClientID, OrderID: stringPointer(reference.OrderID), Status: status,
		ProtectiveOrders: contracts.List[contracts.ProtectiveOrder]{}, FeeQuote: contracts.Zero(), FilledBase: contracts.Zero(),
	}
	filledContracts, err := decimalFromAny(detail.FilledSize)
	if err == nil && filledContracts.IsPositive() {
		result.FilledBase = filledContracts.Mul(spec.ContractValue)
	}
	average, averageErr := decimalFromAny(detail.AveragePrice)
	if averageErr == nil && average.IsPositive() {
		result.AvgPrice = decimalPointer(average)
		if referencePrice.IsPositive() {
			if ratio, ratioErr := average.Sub(referencePrice).Abs().Quo(referencePrice); ratioErr == nil {
				slippage := ratio.Mul(contracts.NewDecimalFromInt64(10_000))
				result.SlippageBPS = decimalPointer(slippage)
			}
		}
	}
	fee, feeErr := decimalFromAny(detail.Fee)
	if feeErr == nil {
		fee = fee.Abs()
		if strings.EqualFold(detail.FeeCurrency, spec.BaseCurrency) && result.AvgPrice != nil {
			fee = fee.Mul(*result.AvgPrice)
		}
		result.FeeQuote = fee
	}
	if status == contracts.OrderStatusFilled {
		result.ProtectiveOrders = append(result.ProtectiveOrders, protective...)
	}
	if status == contracts.OrderStatusCanceled {
		if result.FilledBase.IsPositive() {
			result.Status = contracts.OrderStatusPartiallyFilled
			result.Error = stringPointer("OKX 订单部分成交后，未成交余量已撤销并确认")
		} else {
			result.Error = stringPointer("OKX 订单在成交前被取消")
		}
	}
	return result, nil
}

func (venue *CEXVenue) cancelOKX(ctx context.Context, reference okxOrderRef) error {
	var payload okxOrderAckEnvelope
	if err := venue.okxPrivatePOST(ctx, "/api/v5/trade/cancel-order", map[string]any{
		"instId": reference.InstrumentID, "ordId": reference.OrderID,
	}, &payload); err != nil {
		return err
	}
	if len(payload.Data) > 0 && payload.Data[0].Code != "" && payload.Data[0].Code != "0" {
		return &CEXError{Kind: CEXErrorUpstream, Exchange: venue.id, Operation: "cancel", Code: payload.Data[0].Code, Message: payload.Data[0].Message}
	}
	return nil
}

// ProtectiveOrders reads untriggered conditional orders from the OKX account
// (Demo or explicitly enabled live) so restart reconciliation can fail closed
// when a position lacks a stop loss.
func (venue *CEXVenue) ProtectiveOrders(ctx context.Context, symbol string) ([]contracts.ProtectiveOrder, error) {
	if !venue.TradingEnabled() {
		return nil, &CEXError{Kind: CEXErrorDisabled, Exchange: venue.id, Operation: "protective_orders", Message: ErrCEXTradingDisabled.Error(), Err: ErrCEXTradingDisabled}
	}
	orders := make([]contracts.ProtectiveOrder, 0, 4)
	seen := make(map[string]struct{})
	// OKX classifies a single TP or SL as "conditional", while an attached
	// TP+SL pair is an "oco" algo. Reconciliation must inspect both classes.
	for _, orderType := range []string{"conditional", "oco"} {
		var payload struct {
			Code string `json:"code"`
			Data []struct {
				AlgoID        string `json:"algoId"`
				AlgoClientID  string `json:"algoClOrdId"`
				InstrumentID  string `json:"instId"`
				Side          string `json:"side"`
				PositionSide  string `json:"posSide"`
				ReduceOnly    any    `json:"reduceOnly"`
				CloseFraction any    `json:"closeFraction"`
				Size          any    `json:"sz"`
				StopLoss      any    `json:"slTriggerPx"`
				TakeProfit    any    `json:"tpTriggerPx"`
			} `json:"data"`
		}
		if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/trade/orders-algo-pending", url.Values{
			"ordType": {orderType}, "instId": {okxInstrumentID(symbol)},
		}, true, &payload); err != nil {
			return nil, err
		}
		spec, specErr := venue.okxInstrument(ctx, okxInstrumentID(symbol))
		if specErr != nil {
			return nil, specErr
		}
		for _, row := range payload.Data {
			metadata := okxProtectiveMetadata{
				AlgoID: row.AlgoID, ClientID: row.AlgoClientID, Side: row.Side,
				PositionSide: row.PositionSide, ReduceOnly: row.ReduceOnly,
				CloseFraction: row.CloseFraction, Size: row.Size,
			}
			for _, order := range protectiveOrdersFromOKXAlgo(metadata, row.StopLoss, row.TakeProfit, spec) {
				key := order.Kind + "\x00" + order.OrderID + "\x00" + order.TriggerPrice.String()
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				orders = append(orders, order)
			}
		}
	}
	return orders, nil
}

// PlaceProtectiveOrders submits a standalone reduce-only TP/SL algo for an
// existing position after attached protection failed or went missing. It is
// the deterministic remediation path; callers must re-verify the result with
// ProtectiveOrders because acknowledgement alone does not prove protection.
func (venue *CEXVenue) PlaceProtectiveOrders(
	ctx context.Context,
	clientID string,
	symbol string,
	side contracts.Side,
	marginMode contracts.MarginMode,
	stopLoss *contracts.Decimal,
	takeProfit *contracts.Decimal,
) error {
	if ctx == nil {
		return &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "place_protective", Message: "context is required"}
	}
	if !venue.TradingEnabled() || venue.id != "okx" {
		return &CEXError{Kind: CEXErrorDisabled, Exchange: venue.id, Operation: "place_protective", Message: ErrCEXTradingDisabled.Error(), Err: ErrCEXTradingDisabled}
	}
	if err := venue.checkMutationGuard(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(clientID) == "" {
		return &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "place_protective", Message: "client id is required"}
	}
	hasStop := stopLoss != nil && stopLoss.IsPositive()
	hasTake := takeProfit != nil && takeProfit.IsPositive()
	if !hasStop && !hasTake {
		return &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "place_protective", Message: "protective remediation requires a stop loss or take profit price"}
	}
	if side != contracts.SideLong && side != contracts.SideShort {
		return &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "place_protective", Message: "protective remediation requires a long or short position"}
	}
	instrumentID := okxInstrumentID(symbol)
	positionMode, err := venue.okxPositionMode(ctx)
	if err != nil {
		return err
	}
	if positionMode != "net_mode" {
		return &CEXError{
			Kind: CEXErrorUnsupported, Exchange: venue.id, Operation: "place_protective",
			Message: "full-position protective remediation requires OKX net_mode",
		}
	}
	// The algo closes the position, so its order side is the opposite of the
	// position side. closeFraction=1 avoids re-deriving contract sizes and can
	// never grow the position.
	orderSide := "sell"
	if side == contracts.SideShort {
		orderSide = "buy"
	}
	orderType := "conditional"
	if hasStop && hasTake {
		orderType = "oco"
	}
	body := map[string]any{
		"instId": instrumentID, "tdMode": string(marginMode), "side": orderSide,
		"ordType": orderType, "closeFraction": "1", "reduceOnly": true,
		"algoClOrdId": SanitizeOKXClientID(clientID, "protect"),
	}
	if hasStop {
		body["slTriggerPx"] = stopLoss.String()
		body["slOrdPx"] = "-1"
		body["slTriggerPxType"] = "mark"
	}
	if hasTake {
		body["tpTriggerPx"] = takeProfit.String()
		body["tpOrdPx"] = "-1"
		body["tpTriggerPxType"] = "mark"
	}
	var payload okxOrderAckEnvelope
	if err := venue.okxPrivatePOST(ctx, "/api/v5/trade/order-algo", body, &payload); err != nil {
		return err
	}
	if len(payload.Data) == 0 {
		return &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "place_protective", Message: "empty OKX algo acknowledgement"}
	}
	if ack := payload.Data[0]; ack.Code != "" && ack.Code != "0" {
		return &CEXError{Kind: CEXErrorUpstream, Exchange: venue.id, Operation: "place_protective", Code: ack.Code, Message: ack.Message}
	}
	return nil
}

// CancelProtectiveOrders removes pending TP/SL algos only after the caller has
// verified that the old position is flat. This prevents stale reduce-only
// orders from attaching semantically to a newly opened reverse position.
func (venue *CEXVenue) CancelProtectiveOrders(ctx context.Context, symbol string) error {
	if err := venue.checkMutationGuard(ctx); err != nil {
		return err
	}
	orders, err := venue.ProtectiveOrders(ctx, symbol)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(orders))
	requests := make([]map[string]string, 0, len(orders))
	for _, order := range orders {
		if strings.TrimSpace(order.OrderID) == "" {
			continue
		}
		if _, exists := seen[order.OrderID]; exists {
			continue
		}
		seen[order.OrderID] = struct{}{}
		requests = append(requests, map[string]string{
			"algoId": order.OrderID, "instId": okxInstrumentID(symbol),
		})
	}
	for start := 0; start < len(requests); start += 10 {
		end := min(start+10, len(requests))
		var payload struct {
			Code string `json:"code"`
			Data []struct {
				AlgoID  string `json:"algoId"`
				Code    string `json:"sCode"`
				Message string `json:"sMsg"`
			} `json:"data"`
		}
		if err := venue.okxPrivatePOST(ctx, "/api/v5/trade/cancel-algos", requests[start:end], &payload); err != nil {
			return err
		}
		if len(payload.Data) != end-start {
			return &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "cancel_protective", Message: "incomplete OKX algo cancellation acknowledgement"}
		}
		for _, acknowledgement := range payload.Data {
			if acknowledgement.Code != "" && acknowledgement.Code != "0" {
				return &CEXError{Kind: CEXErrorUpstream, Exchange: venue.id, Operation: "cancel_protective", Code: acknowledgement.Code, Message: acknowledgement.Message}
			}
		}
	}
	return nil
}

type okxProtectiveMetadata struct {
	AlgoID        string
	ClientID      string
	Side          string
	PositionSide  string
	ReduceOnly    any
	CloseFraction any
	Size          any
}

func protectiveOrdersFromOKXAlgo(
	metadata okxProtectiveMetadata,
	stopLoss, takeProfit any,
	spec okxInstrumentSpec,
) []contracts.ProtectiveOrder {
	orders := make([]contracts.ProtectiveOrder, 0, 2)
	positionSide := contracts.Side(strings.ToLower(strings.TrimSpace(metadata.PositionSide)))
	if positionSide != contracts.SideLong && positionSide != contracts.SideShort {
		switch strings.ToLower(strings.TrimSpace(metadata.Side)) {
		case "sell":
			positionSide = contracts.SideLong
		case "buy":
			positionSide = contracts.SideShort
		default:
			positionSide = contracts.SideFlat
		}
	}
	reduceOnly := boolFromAny(metadata.ReduceOnly)
	fullClose := false
	if fraction, err := decimalFromAny(metadata.CloseFraction); err == nil &&
		fraction.Cmp(contracts.NewDecimalFromInt64(1)) == 0 {
		fullClose = true
	}
	sizeBase := contracts.Zero()
	if size, err := decimalFromAny(metadata.Size); err == nil && size.IsPositive() {
		sizeBase = size.Mul(spec.ContractValue)
	}
	makeOrder := func(kind string, price contracts.Decimal) contracts.ProtectiveOrder {
		return contracts.ProtectiveOrder{
			Kind: kind, OrderID: metadata.AlgoID, ClientID: metadata.ClientID,
			PositionSide: positionSide, TriggerPrice: price, SizeBase: sizeBase,
			ReduceOnly: reduceOnly, FullClose: fullClose,
		}
	}
	if price, err := decimalFromAny(stopLoss); err == nil && price.IsPositive() {
		orders = append(orders, makeOrder("stop_loss", price))
	}
	if price, err := decimalFromAny(takeProfit); err == nil && price.IsPositive() {
		orders = append(orders, makeOrder("take_profit", price))
	}
	return orders
}

func boolFromAny(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	default:
		return strings.EqualFold(strings.TrimSpace(fmt.Sprint(value)), "true")
	}
}

func (venue *CEXVenue) okxPrivatePOST(ctx context.Context, path string, body any, target any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return venue.doJSONBody(ctx, http.MethodPost, path, nil, encoded, true, target)
}

func rejectedOKX(clientID, message string) contracts.ExecutionResult {
	return contracts.ExecutionResult{
		ClientID: clientID, Status: contracts.OrderStatusRejected,
		FilledBase: contracts.Zero(), FeeQuote: contracts.Zero(),
		ProtectiveOrders: contracts.List[contracts.ProtectiveOrder]{}, Error: stringPointer(message),
	}
}
