package venue

import (
	"context"
	"net/http"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func (venue *CEXVenue) FetchTicker(ctx context.Context, symbol string) (contracts.Decimal, error) {
	if venue.id == "okx" {
		return venue.fetchOKXTicker(ctx, symbol)
	}
	return venue.fetchBinanceTicker(ctx, symbol)
}

func (venue *CEXVenue) FetchOHLCV(
	ctx context.Context,
	symbol, timeframe string,
	limit int,
) ([]contracts.Candle, error) {
	if venue.id == "okx" {
		return venue.fetchOKXOHLCV(ctx, symbol, timeframe, limit)
	}
	return venue.fetchBinanceOHLCV(ctx, symbol, timeframe, limit)
}

func (venue *CEXVenue) FetchOrderBook(
	ctx context.Context,
	symbol string,
	depth int,
) (contracts.OrderBook, error) {
	if venue.id == "okx" {
		return venue.fetchOKXOrderBook(ctx, symbol, depth)
	}
	return venue.fetchBinanceOrderBook(ctx, symbol, depth)
}

func (venue *CEXVenue) FetchFundingRate(ctx context.Context, symbol string) (contracts.Decimal, error) {
	if venue.id == "okx" {
		return venue.fetchOKXFundingRate(ctx, symbol)
	}
	return venue.fetchBinanceFundingRate(ctx, symbol)
}

func (venue *CEXVenue) FetchOpenInterest(ctx context.Context, symbol string) (contracts.Decimal, error) {
	if venue.id == "okx" {
		return venue.fetchOKXOpenInterest(ctx, symbol)
	}
	return venue.fetchBinanceOpenInterest(ctx, symbol)
}

func (venue *CEXVenue) FetchLongShortRatio(ctx context.Context, symbol string) (contracts.Decimal, error) {
	if venue.id == "okx" {
		return contracts.Zero(), &CEXError{
			Kind: CEXErrorUnsupported, Exchange: venue.id, Operation: "long_short_ratio",
			Message: "OKX long/short ratio endpoint is not enabled", Err: ErrCEXUnsupported,
		}
	}
	return venue.fetchBinanceLongShortRatio(ctx, symbol)
}

func (venue *CEXVenue) Positions(ctx context.Context) ([]contracts.Position, error) {
	if !venue.PrivateConfigured() {
		return []contracts.Position{}, nil
	}
	if venue.id == "okx" {
		return venue.fetchOKXPositions(ctx)
	}
	return venue.fetchBinancePositions(ctx)
}

func (venue *CEXVenue) Balances(ctx context.Context) (contracts.Balances, error) {
	if !venue.PrivateConfigured() {
		return contracts.Balances{QuoteCCY: venue.quoteCurrency}, nil
	}
	if venue.id == "okx" {
		return venue.fetchOKXBalances(ctx)
	}
	return venue.fetchBinanceBalances(ctx)
}

func privateGET(
	venue *CEXVenue,
	ctx context.Context,
	path string,
	target any,
) error {
	return venue.doJSON(ctx, http.MethodGet, path, nil, true, target)
}

func privateFuturesGET(
	venue *CEXVenue,
	ctx context.Context,
	path string,
	target any,
) error {
	return venue.doJSONAt(ctx, venue.futuresBaseURL, http.MethodGet, path, nil, true, target)
}
