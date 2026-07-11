package venue

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func binanceSymbol(symbol string) string {
	base := strings.SplitN(symbol, ":", 2)[0]
	return strings.ToUpper(strings.ReplaceAll(base, "/", ""))
}

func binancePath(symbol, spotPath, futuresPath string) string {
	if strings.Contains(symbol, ":") {
		return futuresPath
	}
	return spotPath
}

func (venue *CEXVenue) fetchBinanceTicker(ctx context.Context, symbol string) (contracts.Decimal, error) {
	path := binancePath(symbol, "/api/v3/ticker/price", "/fapi/v1/ticker/price")
	var payload map[string]any
	err := venue.doJSONAt(ctx, venue.binanceBaseURL(symbol), http.MethodGet, path,
		url.Values{"symbol": {binanceSymbol(symbol)}}, false, &payload)
	if err != nil {
		return contracts.Zero(), err
	}
	price, err := decimalFromAny(payload["price"])
	if err != nil || !price.IsPositive() {
		return contracts.Zero(), &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "ticker", Message: "missing or invalid price", Err: err}
	}
	return price, nil
}

func (venue *CEXVenue) fetchBinanceOHLCV(
	ctx context.Context,
	symbol, timeframe string,
	limit int,
) ([]contracts.Candle, error) {
	if !validBinanceTimeframe(timeframe) || limit <= 0 || limit > 1000 {
		return nil, &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "ohlcv", Message: "unsupported timeframe or limit"}
	}
	path := binancePath(symbol, "/api/v3/klines", "/fapi/v1/klines")
	var rows [][]any
	err := venue.doJSONAt(ctx, venue.binanceBaseURL(symbol), http.MethodGet, path, url.Values{
		"symbol": {binanceSymbol(symbol)}, "interval": {timeframe}, "limit": {strconv.Itoa(limit)},
	}, false, &rows)
	if err != nil {
		return nil, err
	}
	result := make([]contracts.Candle, 0, len(rows))
	for _, row := range rows {
		if len(row) < 6 {
			return nil, &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "ohlcv", Message: "short kline row"}
		}
		stamp, parseErr := milliseconds(row[0])
		if parseErr != nil {
			return nil, &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "ohlcv", Message: "invalid timestamp", Err: parseErr}
		}
		values := make([]contracts.Decimal, 5)
		for index := range values {
			values[index], parseErr = decimalFromAny(row[index+1])
			if parseErr != nil {
				return nil, &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "ohlcv", Message: "invalid decimal", Err: parseErr}
			}
		}
		result = append(result, contracts.Candle{
			TS: stamp, Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[4],
		})
	}
	sortCandles(result)
	return result, nil
}

func validBinanceTimeframe(value string) bool {
	switch value {
	case "1m", "5m", "15m", "30m", "1h", "4h", "1d":
		return true
	default:
		return false
	}
}

func (venue *CEXVenue) fetchBinanceOrderBook(
	ctx context.Context,
	symbol string,
	depth int,
) (contracts.OrderBook, error) {
	if depth <= 0 || depth > 5000 {
		return contracts.OrderBook{}, &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "orderbook", Message: "depth out of range"}
	}
	path := binancePath(symbol, "/api/v3/depth", "/fapi/v1/depth")
	var payload struct {
		Bids [][]any `json:"bids"`
		Asks [][]any `json:"asks"`
	}
	if err := venue.doJSONAt(ctx, venue.binanceBaseURL(symbol), http.MethodGet, path, url.Values{
		"symbol": {binanceSymbol(symbol)}, "limit": {strconv.Itoa(depth)},
	}, false, &payload); err != nil {
		return contracts.OrderBook{}, err
	}
	bids, err := parsePriceLevels(payload.Bids)
	if err != nil {
		return contracts.OrderBook{}, err
	}
	asks, err := parsePriceLevels(payload.Asks)
	if err != nil {
		return contracts.OrderBook{}, err
	}
	return contracts.OrderBook{Bids: bids, Asks: asks}, nil
}

func parsePriceLevels(rows [][]any) (contracts.List[contracts.PriceLevel], error) {
	levels := make(contracts.List[contracts.PriceLevel], 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			return nil, &CEXError{Kind: CEXErrorDecode, Operation: "orderbook", Message: "short price level"}
		}
		price, err := decimalFromAny(row[0])
		if err != nil {
			return nil, err
		}
		size, err := decimalFromAny(row[1])
		if err != nil {
			return nil, err
		}
		levels = append(levels, contracts.PriceLevel{price, size})
	}
	return levels, nil
}

func (venue *CEXVenue) fetchBinanceFundingRate(ctx context.Context, symbol string) (contracts.Decimal, error) {
	var payload map[string]any
	if err := venue.doJSONAt(ctx, venue.futuresBaseURL, http.MethodGet, "/fapi/v1/premiumIndex", url.Values{
		"symbol": {binanceSymbol(symbol)},
	}, false, &payload); err != nil {
		return contracts.Zero(), err
	}
	return decimalFromAny(payload["lastFundingRate"])
}

func (venue *CEXVenue) fetchBinanceOpenInterest(ctx context.Context, symbol string) (contracts.Decimal, error) {
	var payload map[string]any
	if err := venue.doJSONAt(ctx, venue.futuresBaseURL, http.MethodGet, "/fapi/v1/openInterest", url.Values{
		"symbol": {binanceSymbol(symbol)},
	}, false, &payload); err != nil {
		return contracts.Zero(), err
	}
	return decimalFromAny(payload["openInterest"])
}

func (venue *CEXVenue) fetchBinanceLongShortRatio(ctx context.Context, symbol string) (contracts.Decimal, error) {
	var rows []map[string]any
	if err := venue.doJSONAt(ctx, venue.futuresBaseURL, http.MethodGet, "/futures/data/globalLongShortAccountRatio", url.Values{
		"symbol": {binanceSymbol(symbol)}, "period": {"5m"}, "limit": {"1"},
	}, false, &rows); err != nil {
		return contracts.Zero(), err
	}
	if len(rows) == 0 {
		return contracts.Zero(), &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "long_short_ratio", Message: "empty response"}
	}
	return decimalFromAny(rows[len(rows)-1]["longShortRatio"])
}

func (venue *CEXVenue) fetchBinanceBalances(ctx context.Context) (contracts.Balances, error) {
	var payload struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   any    `json:"free"`
			Locked any    `json:"locked"`
		} `json:"balances"`
	}
	if err := privateGET(venue, ctx, "/api/v3/account", &payload); err != nil {
		return contracts.Balances{}, err
	}
	result := contracts.Balances{QuoteCCY: venue.quoteCurrency}
	for _, balance := range payload.Balances {
		if balance.Asset != venue.quoteCurrency {
			continue
		}
		free, err := decimalFromAny(balance.Free)
		if err != nil {
			return contracts.Balances{}, err
		}
		locked, err := decimalFromAny(balance.Locked)
		if err != nil {
			return contracts.Balances{}, err
		}
		result.FreeQuote = free
		result.TotalQuote = free.Add(locked)
		break
	}
	return result, nil
}

func (venue *CEXVenue) fetchBinancePositions(ctx context.Context) ([]contracts.Position, error) {
	var rows []map[string]any
	if err := privateFuturesGET(venue, ctx, "/fapi/v2/positionRisk", &rows); err != nil {
		return nil, err
	}
	positions := make([]contracts.Position, 0)
	for _, row := range rows {
		amount, err := decimalFromAny(row["positionAmt"])
		if err != nil || amount.IsZero() {
			continue
		}
		entry, err := decimalFromAny(row["entryPrice"])
		if err != nil {
			return nil, err
		}
		leverageDecimal, err := decimalFromAny(row["leverage"])
		if err != nil {
			return nil, err
		}
		leverage, err := leverageDecimal.Float64()
		if err != nil {
			return nil, err
		}
		side := contracts.SideLong
		if amount.IsNegative() {
			side = contracts.SideShort
			amount = amount.Abs()
		}
		position := contracts.Position{
			Symbol: binanceDisplaySymbol(fmt.Sprint(row["symbol"]), venue.quoteCurrency), Venue: venue.id,
			Side: side, Instrument: contracts.InstrumentPerp, SizeBase: amount,
			EntryPrice: entry, Leverage: leverage,
		}
		if value, parseErr := decimalFromAny(row["liquidationPrice"]); parseErr == nil && value.IsPositive() {
			position.LiqPrice = decimalPointer(value)
		}
		if margin := fmt.Sprint(row["marginType"]); margin == "isolated" || margin == "cross" {
			mode := contracts.MarginMode(margin)
			position.MarginMode = &mode
		}
		positions = append(positions, position)
	}
	return positions, nil
}

func binanceDisplaySymbol(raw, quote string) string {
	raw = strings.ToUpper(raw)
	quote = strings.ToUpper(quote)
	if strings.HasSuffix(raw, quote) {
		return strings.TrimSuffix(raw, quote) + "/" + quote + ":" + quote
	}
	return raw
}

func (venue *CEXVenue) binanceBaseURL(symbol string) string {
	if strings.Contains(symbol, ":") {
		return venue.futuresBaseURL
	}
	return venue.baseURL
}

// Ensure json.Number remains referenced in this adapter when endpoint fixture
// payloads use numeric rather than string fields.
var _ = json.Number("")
