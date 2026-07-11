package data

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type SnapshotVenue interface {
	ID() string
	FetchOHLCV(context.Context, string, string, int) ([]contracts.Candle, error)
	FetchOrderBook(context.Context, string, int) (contracts.OrderBook, error)
}

type fundingVenue interface {
	FetchFundingRate(context.Context, string) (contracts.Decimal, error)
}

type openInterestVenue interface {
	FetchOpenInterest(context.Context, string) (contracts.Decimal, error)
}

type longShortVenue interface {
	FetchLongShortRatio(context.Context, string) (contracts.Decimal, error)
}

type CEXMarketData struct {
	venue SnapshotVenue
	now   func() time.Time
}

func NewCEXMarketData(venue SnapshotVenue) *CEXMarketData {
	return &CEXMarketData{venue: venue, now: time.Now}
}

func (source *CEXMarketData) Snapshot(
	ctx context.Context,
	symbol string,
) (contracts.MarketSnapshot, error) {
	if source == nil || source.venue == nil {
		return contracts.MarketSnapshot{}, errors.New("cex market data requires a venue")
	}
	type candleResult struct {
		value []contracts.Candle
		err   error
	}
	type orderBookResult struct {
		value contracts.OrderBook
		err   error
	}
	candlesC := make(chan candleResult, 1)
	orderBookC := make(chan orderBookResult, 1)
	go func() {
		value, err := source.venue.FetchOHLCV(ctx, symbol, "1h", 200)
		candlesC <- candleResult{value: value, err: err}
	}()
	go func() {
		value, err := source.venue.FetchOrderBook(ctx, symbol, 20)
		orderBookC <- orderBookResult{value: value, err: err}
	}()

	derivativesC := make(chan *contracts.DerivativesData, 1)
	if strings.Contains(symbol, ":") {
		go func() { derivativesC <- source.derivatives(ctx, symbol) }()
	} else {
		derivativesC <- nil
	}
	candles := <-candlesC
	orderBook := <-orderBookC
	derivatives := <-derivativesC
	if candles.err != nil {
		return contracts.MarketSnapshot{}, candles.err
	}
	var orderBookPointer *contracts.OrderBook
	if orderBook.err == nil {
		value := orderBook.value
		orderBookPointer = &value
	}
	return contracts.MarketSnapshot{
		Symbol:      symbol,
		Venue:       source.venue.ID(),
		TS:          source.now().UTC(),
		OHLCV:       contracts.List[contracts.Candle](candles.value),
		OrderBook:   orderBookPointer,
		Derivatives: derivatives,
	}, nil
}

func (source *CEXMarketData) derivatives(
	ctx context.Context,
	symbol string,
) *contracts.DerivativesData {
	fundingSource, ok := source.venue.(fundingVenue)
	if !ok {
		return nil
	}
	type decimalResult struct {
		value contracts.Decimal
		err   error
	}
	fundingC := make(chan decimalResult, 1)
	openInterestC := make(chan decimalResult, 1)
	longShortC := make(chan decimalResult, 1)
	go func() {
		value, err := fundingSource.FetchFundingRate(ctx, symbol)
		fundingC <- decimalResult{value: value, err: err}
	}()
	go func() {
		if venue, supported := source.venue.(openInterestVenue); supported {
			value, err := venue.FetchOpenInterest(ctx, symbol)
			openInterestC <- decimalResult{value: value, err: err}
			return
		}
		openInterestC <- decimalResult{err: errors.New("unsupported")}
	}()
	go func() {
		if venue, supported := source.venue.(longShortVenue); supported {
			value, err := venue.FetchLongShortRatio(ctx, symbol)
			longShortC <- decimalResult{value: value, err: err}
			return
		}
		longShortC <- decimalResult{err: errors.New("unsupported")}
	}()
	funding := <-fundingC
	openInterest := <-openInterestC
	longShort := <-longShortC
	if funding.err != nil {
		return nil
	}
	result := &contracts.DerivativesData{FundingRate: decimalPointer(funding.value)}
	if openInterest.err == nil {
		result.OpenInterest = decimalPointer(openInterest.value)
	}
	if longShort.err == nil {
		result.LongShortRatio = decimalPointer(longShort.value)
	}
	return result
}

type OnchainFetcher func(context.Context, string) (*contracts.OnchainData, error)

type OnchainDataSource struct{ fetcher OnchainFetcher }

func NewOnchainDataSource(fetcher OnchainFetcher) *OnchainDataSource {
	return &OnchainDataSource{fetcher: fetcher}
}

func (source *OnchainDataSource) IsConfigured() bool {
	return source != nil && source.fetcher != nil
}

func (source *OnchainDataSource) Fetch(ctx context.Context, symbol string) *contracts.OnchainData {
	if !source.IsConfigured() {
		return nil
	}
	value, err := source.fetcher(ctx, symbol)
	if err != nil {
		return nil
	}
	return value
}

func BuildSource(kind string, venue SnapshotVenue) (Source, error) {
	if kind == "cex" {
		if venue == nil {
			return nil, errors.New("cex 行情源需要传入只读 venue")
		}
		return NewCEXMarketData(venue), nil
	}
	return NewSyntheticMarketData(), nil
}

func decimalPointer(value contracts.Decimal) *contracts.Decimal { return &value }
