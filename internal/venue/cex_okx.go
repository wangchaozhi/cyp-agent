package venue

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func okxInstrumentID(symbol string) string {
	parts := strings.SplitN(symbol, ":", 2)
	spot := strings.ToUpper(strings.ReplaceAll(parts[0], "/", "-"))
	if len(parts) == 2 {
		return spot + "-SWAP"
	}
	return spot
}

type okxTickerEnvelope struct {
	Code string `json:"code"`
	Data []struct {
		Last any `json:"last"`
	} `json:"data"`
}

func (venue *CEXVenue) fetchOKXTicker(ctx context.Context, symbol string) (contracts.Decimal, error) {
	var payload okxTickerEnvelope
	if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/market/ticker", url.Values{
		"instId": {okxInstrumentID(symbol)},
	}, false, &payload); err != nil {
		return contracts.Zero(), err
	}
	if len(payload.Data) == 0 {
		return contracts.Zero(), &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "ticker", Message: "empty OKX ticker"}
	}
	price, err := decimalFromAny(payload.Data[0].Last)
	if err != nil || !price.IsPositive() {
		return contracts.Zero(), &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "ticker", Message: "invalid OKX ticker", Err: err}
	}
	return price, nil
}

func (venue *CEXVenue) fetchOKXOHLCV(
	ctx context.Context,
	symbol, timeframe string,
	limit int,
) ([]contracts.Candle, error) {
	bar, ok := okxTimeframe(timeframe)
	if !ok || limit <= 0 || limit > 300 {
		return nil, &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "ohlcv", Message: "unsupported timeframe or limit"}
	}
	var payload struct {
		Code string  `json:"code"`
		Data [][]any `json:"data"`
	}
	if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/market/candles", url.Values{
		"instId": {okxInstrumentID(symbol)}, "bar": {bar}, "limit": {strconv.Itoa(limit)},
	}, false, &payload); err != nil {
		return nil, err
	}
	return parseOKXCandles(venue.id, payload.Data)
}

func (venue *CEXVenue) fetchOKXOHLCVRange(
	ctx context.Context,
	symbol, timeframe string,
	start, end time.Time,
	pageSize int,
) ([]contracts.Candle, error) {
	bar, ok := okxTimeframe(timeframe)
	if !ok || !start.Before(end) || pageSize <= 0 || pageSize > 300 {
		return nil, &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "ohlcv_range", Message: "unsupported timeframe, range, or page size"}
	}
	start, end = start.UTC(), end.UTC()
	cursor := end
	result := make([]contracts.Candle, 0)
	for cursor.After(start) {
		var payload struct {
			Code string  `json:"code"`
			Data [][]any `json:"data"`
		}
		if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/market/history-candles", url.Values{
			"instId": {okxInstrumentID(symbol)}, "bar": {bar}, "limit": {strconv.Itoa(pageSize)},
			"after": {strconv.FormatInt(cursor.UnixMilli(), 10)},
		}, false, &payload); err != nil {
			return nil, err
		}
		page, err := parseOKXCandles(venue.id, payload.Data)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, candle := range page {
			if !candle.TS.Before(start) && candle.TS.Before(end) {
				result = append(result, candle)
			}
		}
		oldest := page[0].TS
		if !oldest.Before(cursor) || !oldest.After(start) || len(page) < pageSize {
			break
		}
		cursor = oldest
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(120 * time.Millisecond):
		}
	}
	return uniqueSortedCandles(result), nil
}

func parseOKXCandles(exchange string, rows [][]any) ([]contracts.Candle, error) {
	candles := make([]contracts.Candle, 0, len(rows))
	for _, row := range rows {
		if len(row) < 6 {
			return nil, &CEXError{Kind: CEXErrorDecode, Exchange: exchange, Operation: "ohlcv", Message: "short OKX candle row"}
		}
		stamp, err := milliseconds(row[0])
		if err != nil {
			return nil, &CEXError{Kind: CEXErrorDecode, Exchange: exchange, Operation: "ohlcv", Message: "invalid OKX timestamp", Err: err}
		}
		values := make([]contracts.Decimal, 5)
		for index := range values {
			values[index], err = decimalFromAny(row[index+1])
			if err != nil {
				return nil, &CEXError{Kind: CEXErrorDecode, Exchange: exchange, Operation: "ohlcv", Message: "invalid OKX decimal", Err: err}
			}
		}
		candles = append(candles, contracts.Candle{
			TS: stamp, Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[4],
		})
	}
	sortCandles(candles)
	return candles, nil
}

func okxTimeframe(value string) (string, bool) {
	switch value {
	case "1m", "5m", "15m", "30m":
		return value, true
	case "1h":
		return "1H", true
	case "4h":
		return "4H", true
	case "1d":
		return "1Dutc", true
	default:
		return "", false
	}
}

func (venue *CEXVenue) fetchOKXOrderBook(
	ctx context.Context,
	symbol string,
	depth int,
) (contracts.OrderBook, error) {
	if depth <= 0 || depth > 400 {
		return contracts.OrderBook{}, &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "orderbook", Message: "depth out of range"}
	}
	var payload struct {
		Code string `json:"code"`
		Data []struct {
			Bids [][]any `json:"bids"`
			Asks [][]any `json:"asks"`
		} `json:"data"`
	}
	if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/market/books", url.Values{
		"instId": {okxInstrumentID(symbol)}, "sz": {strconv.Itoa(depth)},
	}, false, &payload); err != nil {
		return contracts.OrderBook{}, err
	}
	if len(payload.Data) == 0 {
		return contracts.OrderBook{}, &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "orderbook", Message: "empty OKX order book"}
	}
	bids, err := parsePriceLevels(payload.Data[0].Bids)
	if err != nil {
		return contracts.OrderBook{}, err
	}
	asks, err := parsePriceLevels(payload.Data[0].Asks)
	if err != nil {
		return contracts.OrderBook{}, err
	}
	return contracts.OrderBook{Bids: bids, Asks: asks}, nil
}

func (venue *CEXVenue) fetchOKXFundingRate(ctx context.Context, symbol string) (contracts.Decimal, error) {
	var payload struct {
		Code string `json:"code"`
		Data []struct {
			FundingRate any `json:"fundingRate"`
		} `json:"data"`
	}
	if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/public/funding-rate", url.Values{
		"instId": {okxInstrumentID(symbol)},
	}, false, &payload); err != nil {
		return contracts.Zero(), err
	}
	if len(payload.Data) == 0 {
		return contracts.Zero(), &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "funding_rate", Message: "empty OKX funding response"}
	}
	return decimalFromAny(payload.Data[0].FundingRate)
}

func (venue *CEXVenue) fetchOKXOpenInterest(ctx context.Context, symbol string) (contracts.Decimal, error) {
	var payload struct {
		Code string `json:"code"`
		Data []struct {
			OpenInterestCurrency any `json:"oiCcy"`
			OpenInterest         any `json:"oi"`
		} `json:"data"`
	}
	if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/public/open-interest", url.Values{
		"instId": {okxInstrumentID(symbol)}, "instType": {"SWAP"},
	}, false, &payload); err != nil {
		return contracts.Zero(), err
	}
	if len(payload.Data) == 0 {
		return contracts.Zero(), &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "open_interest", Message: "empty OKX open interest response"}
	}
	if value, err := decimalFromAny(payload.Data[0].OpenInterestCurrency); err == nil {
		return value, nil
	}
	return decimalFromAny(payload.Data[0].OpenInterest)
}

func (venue *CEXVenue) fetchOKXBalances(ctx context.Context) (contracts.Balances, error) {
	var payload struct {
		Code string `json:"code"`
		Data []struct {
			Details []struct {
				Currency  string `json:"ccy"`
				Available any    `json:"availBal"`
				Equity    any    `json:"eq"`
				Cash      any    `json:"cashBal"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := privateGET(venue, ctx, "/api/v5/account/balance", &payload); err != nil {
		return contracts.Balances{}, err
	}
	result := contracts.Balances{QuoteCCY: venue.quoteCurrency}
	if len(payload.Data) == 0 {
		return result, nil
	}
	for _, balance := range payload.Data[0].Details {
		if balance.Currency != venue.quoteCurrency {
			continue
		}
		free, err := decimalFromAny(balance.Available)
		if err != nil {
			free, err = decimalFromAny(balance.Cash)
		}
		if err != nil {
			return contracts.Balances{}, err
		}
		total, err := decimalFromAny(balance.Equity)
		if err != nil {
			total = free
		}
		result.FreeQuote, result.TotalQuote = free, total
		break
	}
	return result, nil
}

func (venue *CEXVenue) fetchOKXPositions(ctx context.Context) ([]contracts.Position, error) {
	var payload struct {
		Code string `json:"code"`
		Data []struct {
			InstrumentID string `json:"instId"`
			Position     any    `json:"pos"`
			PositionSide string `json:"posSide"`
			AveragePrice any    `json:"avgPx"`
			Leverage     any    `json:"lever"`
			Liquidation  any    `json:"liqPx"`
			MarginMode   string `json:"mgnMode"`
		} `json:"data"`
	}
	if err := privateGET(venue, ctx, "/api/v5/account/positions", &payload); err != nil {
		return nil, err
	}
	positions := make([]contracts.Position, 0, len(payload.Data))
	for _, row := range payload.Data {
		contractAmount, err := decimalFromAny(row.Position)
		if err != nil || contractAmount.IsZero() {
			continue
		}
		side := contracts.Side(row.PositionSide)
		if side != contracts.SideLong && side != contracts.SideShort {
			side = contracts.SideLong
			if contractAmount.IsNegative() {
				side = contracts.SideShort
			}
		}
		spec, err := venue.okxInstrument(ctx, row.InstrumentID)
		if err != nil {
			return nil, err
		}
		amount := contractAmount.Abs().Mul(spec.ContractValue)
		entry, err := decimalFromAny(row.AveragePrice)
		if err != nil {
			return nil, err
		}
		leverageDecimal, err := decimalFromAny(row.Leverage)
		if err != nil {
			return nil, err
		}
		leverage, err := leverageDecimal.Float64()
		if err != nil {
			return nil, err
		}
		position := contracts.Position{
			Symbol: okxDisplaySymbol(row.InstrumentID), Venue: venue.id, Side: side,
			Instrument: contracts.InstrumentPerp, SizeBase: amount, EntryPrice: entry, Leverage: leverage,
		}
		if liquidation, parseErr := decimalFromAny(row.Liquidation); parseErr == nil && liquidation.IsPositive() {
			position.LiqPrice = decimalPointer(liquidation)
		}
		if row.MarginMode == "isolated" || row.MarginMode == "cross" {
			mode := contracts.MarginMode(row.MarginMode)
			position.MarginMode = &mode
		}
		positions = append(positions, position)
	}
	return positions, nil
}

func okxDisplaySymbol(instrument string) string {
	parts := strings.Split(strings.ToUpper(instrument), "-")
	if len(parts) >= 3 && parts[len(parts)-1] == "SWAP" {
		return fmt.Sprintf("%s/%s:%s", parts[0], parts[1], parts[1])
	}
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return instrument
}
