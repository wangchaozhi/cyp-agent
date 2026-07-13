package venue

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

const okxOrderPollAttempts = 12

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

func (venue *CEXVenue) placeOKXDemo(
	ctx context.Context,
	intent contracts.OrderIntent,
) (contracts.ExecutionResult, okxOrderRef, error) {
	if ctx == nil {
		return contracts.ExecutionResult{}, okxOrderRef{}, &CEXError{
			Kind: CEXErrorValidation, Exchange: venue.id, Operation: "place", Message: "context is required",
		}
	}
	if !strings.EqualFold(strings.TrimSpace(intent.Venue), "okx") {
		return rejectedOKXDemo(intent.ClientID, "订单场所与 OKX Demo 执行器不匹配"), okxOrderRef{}, nil
	}
	if intent.Instrument != contracts.InstrumentPerp ||
		!strings.HasSuffix(strings.ToUpper(strings.TrimSpace(intent.Symbol)), ":USDT") {
		return rejectedOKXDemo(intent.ClientID, "OKX Demo 当前仅开放 USDT 永续合约执行"), okxOrderRef{}, nil
	}
	if intent.Side != contracts.SideLong && intent.Side != contracts.SideShort {
		return rejectedOKXDemo(intent.ClientID, "OKX Demo 下单方向必须为 long 或 short"), okxOrderRef{}, nil
	}
	if !intent.SizeQuote.IsPositive() {
		return rejectedOKXDemo(intent.ClientID, "OKX Demo 下单金额必须大于 0"), okxOrderRef{}, nil
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
		return rejectedOKXDemo(intent.ClientID, err.Error()), okxOrderRef{}, nil
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
			return rejectedOKXDemo(intent.ClientID, "OKX Demo 限价无效"), okxOrderRef{}, nil
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
		return contracts.ExecutionResult{}, okxOrderRef{}, err
	}
	if len(accepted.Data) == 0 {
		return contracts.ExecutionResult{}, okxOrderRef{}, &CEXError{
			Kind: CEXErrorDecode, Exchange: venue.id, Operation: "place", Message: "empty OKX order acknowledgement",
		}
	}
	ack := accepted.Data[0]
	if ack.Code != "" && ack.Code != "0" {
		message := strings.TrimSpace(ack.Message)
		if message == "" {
			message = "OKX Demo 拒绝订单"
		}
		return rejectedOKXDemo(intent.ClientID, message), okxOrderRef{}, nil
	}
	if ack.OrderID == "" {
		return contracts.ExecutionResult{}, okxOrderRef{}, &CEXError{
			Kind: CEXErrorDecode, Exchange: venue.id, Operation: "place", Message: "OKX acknowledgement omitted ordId",
		}
	}
	reference := okxOrderRef{InstrumentID: instrumentID, OrderID: ack.OrderID}
	result, err := venue.waitOKXOrder(ctx, intent, referencePrice, spec, reference, protective)
	if err != nil {
		return contracts.ExecutionResult{}, reference, err
	}
	return result, reference, nil
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
		return contracts.Zero(), fmt.Errorf("OKX Demo 无法从无效金额或价格换算合约张数")
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
		return contracts.Zero(), fmt.Errorf("OKX Demo 下单张数 %s 低于最小值 %s", contractsSize.String(), spec.MinSize.String())
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
) (contracts.ExecutionResult, error) {
	var last okxOrderDetail
	for attempt := 0; attempt < okxOrderPollAttempts; attempt++ {
		var payload okxOrderDetailEnvelope
		if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/trade/order", url.Values{
			"instId": {reference.InstrumentID}, "ordId": {reference.OrderID},
		}, true, &payload); err != nil {
			return contracts.ExecutionResult{}, err
		}
		if len(payload.Data) == 0 {
			return contracts.ExecutionResult{}, &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "order", Message: "empty OKX order detail"}
		}
		last = payload.Data[0]
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
	return okxExecutionResult(intent, referencePrice, spec, reference, protective, last)
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
		result.Error = stringPointer("OKX Demo 订单在完全成交前被取消")
	}
	return result, nil
}

func (venue *CEXVenue) cancelOKXDemo(ctx context.Context, reference okxOrderRef) error {
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

// ProtectiveOrders reads untriggered conditional orders from the OKX Demo
// account so restart reconciliation can fail closed when a position lacks a
// stop loss.
func (venue *CEXVenue) ProtectiveOrders(ctx context.Context, symbol string) ([]contracts.ProtectiveOrder, error) {
	if !venue.DemoTradingEnabled() {
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
				AlgoID     string `json:"algoId"`
				StopLoss   any    `json:"slTriggerPx"`
				TakeProfit any    `json:"tpTriggerPx"`
			} `json:"data"`
		}
		if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/trade/orders-algo-pending", url.Values{
			"ordType": {orderType}, "instId": {okxInstrumentID(symbol)},
		}, true, &payload); err != nil {
			return nil, err
		}
		for _, row := range payload.Data {
			for _, order := range protectiveOrdersFromOKXAlgo(row.AlgoID, row.StopLoss, row.TakeProfit) {
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

func protectiveOrdersFromOKXAlgo(algoID string, stopLoss, takeProfit any) []contracts.ProtectiveOrder {
	orders := make([]contracts.ProtectiveOrder, 0, 2)
	if price, err := decimalFromAny(stopLoss); err == nil && price.IsPositive() {
		orders = append(orders, contracts.ProtectiveOrder{
			Kind: "stop_loss", OrderID: algoID, TriggerPrice: price, ReduceOnly: true,
		})
	}
	if price, err := decimalFromAny(takeProfit); err == nil && price.IsPositive() {
		orders = append(orders, contracts.ProtectiveOrder{
			Kind: "take_profit", OrderID: algoID, TriggerPrice: price, ReduceOnly: true,
		})
	}
	return orders
}

func (venue *CEXVenue) okxPrivatePOST(ctx context.Context, path string, body any, target any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return venue.doJSONBody(ctx, http.MethodPost, path, nil, encoded, true, target)
}

func rejectedOKXDemo(clientID, message string) contracts.ExecutionResult {
	return contracts.ExecutionResult{
		ClientID: clientID, Status: contracts.OrderStatusRejected,
		FilledBase: contracts.Zero(), FeeQuote: contracts.Zero(),
		ProtectiveOrders: contracts.List[contracts.ProtectiveOrder]{}, Error: stringPointer(message),
	}
}
