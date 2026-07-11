package data

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type TickerVenue interface {
	ID() string
	FetchTicker(context.Context, string) (contracts.Decimal, error)
}

type FundingVenue interface {
	FetchFundingRate(context.Context, string) (contracts.Decimal, error)
}

type BestQuote struct {
	Venue *string            `json:"venue"`
	Price *contracts.Decimal `json:"price"`
}

type MarketSummary struct {
	Symbol       string                       `json:"symbol"`
	Tickers      map[string]contracts.Decimal `json:"tickers"`
	BestBuy      BestQuote                    `json:"best_buy"`
	BestSell     BestQuote                    `json:"best_sell"`
	SpreadBPS    *contracts.Decimal           `json:"spread_bps"`
	FundingRates map[string]contracts.Decimal `json:"funding_rates"`
	ArbHints     contracts.List[string]       `json:"arb_hints"`
}

type MarketAggregator struct{ venues []TickerVenue }

func NewMarketAggregator(venues []TickerVenue) *MarketAggregator {
	return &MarketAggregator{venues: append([]TickerVenue(nil), venues...)}
}

// Tickers isolates every upstream failure and waits for venues concurrently.
func (aggregator *MarketAggregator) Tickers(
	ctx context.Context,
	symbol string,
) map[string]contracts.Decimal {
	type result struct {
		id    string
		price contracts.Decimal
		err   error
	}
	results := make(chan result, len(aggregator.venues))
	var wait sync.WaitGroup
	for _, current := range aggregator.venues {
		if current == nil {
			continue
		}
		wait.Add(1)
		go func(current TickerVenue) {
			defer wait.Done()
			price, err := current.FetchTicker(ctx, symbol)
			results <- result{id: current.ID(), price: price, err: err}
		}(current)
	}
	wait.Wait()
	close(results)
	prices := make(map[string]contracts.Decimal)
	for current := range results {
		if current.err == nil && current.price.IsPositive() {
			prices[current.id] = current.price
		}
	}
	return prices
}

// FundingRates converts spot notation to the exchange perpetual
// symbol and skips unsupported/failing venues.
func (aggregator *MarketAggregator) FundingRates(
	ctx context.Context,
	symbol string,
) map[string]contracts.Decimal {
	perpetual := perpetualSymbol(symbol)
	type result struct {
		id   string
		rate contracts.Decimal
		err  error
	}
	results := make(chan result, len(aggregator.venues))
	var wait sync.WaitGroup
	for _, current := range aggregator.venues {
		funding, ok := current.(FundingVenue)
		if !ok {
			continue
		}
		wait.Add(1)
		go func(id string, funding FundingVenue) {
			defer wait.Done()
			rate, err := funding.FetchFundingRate(ctx, perpetual)
			results <- result{id: id, rate: rate, err: err}
		}(current.ID(), funding)
	}
	wait.Wait()
	close(results)
	rates := make(map[string]contracts.Decimal)
	for current := range results {
		if current.err == nil {
			rates[current.id] = current.rate
		}
	}
	return rates
}

func (aggregator *MarketAggregator) BestVenue(
	ctx context.Context,
	symbol string,
	side contracts.Side,
) BestQuote {
	return aggregator.best(aggregator.Tickers(ctx, symbol), side)
}

func (aggregator *MarketAggregator) SpreadBPS(
	ctx context.Context,
	symbol string,
) *contracts.Decimal {
	return spreadBPS(aggregator.Tickers(ctx, symbol))
}

// Summary fetches each upstream dimension exactly once, concurrently, and
// derives one internally consistent market view.
func (aggregator *MarketAggregator) Summary(
	ctx context.Context,
	symbol string,
) MarketSummary {
	var tickers map[string]contracts.Decimal
	var funding map[string]contracts.Decimal
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		tickers = aggregator.Tickers(ctx, symbol)
	}()
	go func() {
		defer wait.Done()
		funding = aggregator.FundingRates(ctx, symbol)
	}()
	wait.Wait()
	spreadThreshold := contracts.MustDecimal("10")
	fundingThreshold := contracts.MustDecimal("0.0003")
	return MarketSummary{
		Symbol:       symbol,
		Tickers:      tickers,
		BestBuy:      aggregator.best(tickers, contracts.SideLong),
		BestSell:     aggregator.best(tickers, contracts.SideShort),
		SpreadBPS:    spreadBPS(tickers),
		FundingRates: funding,
		ArbHints: aggregator.hints(
			symbol, tickers, funding, spreadThreshold, fundingThreshold,
		),
	}
}

func (aggregator *MarketAggregator) best(
	prices map[string]contracts.Decimal,
	side contracts.Side,
) BestQuote {
	var selectedID string
	var selected contracts.Decimal
	found := false
	for _, current := range aggregator.venues {
		if current == nil {
			continue
		}
		price, ok := prices[current.ID()]
		if !ok {
			continue
		}
		if !found || (side == contracts.SideLong && price.Cmp(selected) < 0) ||
			(side != contracts.SideLong && price.Cmp(selected) > 0) {
			selectedID, selected, found = current.ID(), price, true
		}
	}
	if !found {
		return BestQuote{}
	}
	return BestQuote{Venue: stringPointer(selectedID), Price: decimalPointer(selected)}
}

func spreadBPS(prices map[string]contracts.Decimal) *contracts.Decimal {
	if len(prices) < 2 {
		return nil
	}
	var lowest, highest contracts.Decimal
	first := true
	for _, price := range prices {
		if first || price.Cmp(lowest) < 0 {
			lowest = price
		}
		if first || price.Cmp(highest) > 0 {
			highest = price
		}
		first = false
	}
	if !lowest.IsPositive() {
		return nil
	}
	ratio, err := highest.Sub(lowest).Quo(lowest)
	if err != nil {
		return nil
	}
	value := ratio.Mul(contracts.NewDecimalFromInt64(10_000))
	return decimalPointer(value)
}

func (aggregator *MarketAggregator) hints(
	symbol string,
	tickers map[string]contracts.Decimal,
	funding map[string]contracts.Decimal,
	spreadThreshold contracts.Decimal,
	fundingThreshold contracts.Decimal,
) contracts.List[string] {
	hints := make(contracts.List[string], 0)
	if spread := spreadBPS(tickers); spread != nil && spread.Cmp(spreadThreshold) >= 0 {
		buy := aggregator.best(tickers, contracts.SideLong)
		sell := aggregator.best(tickers, contracts.SideShort)
		hints = append(hints, fmt.Sprintf(
			"%s 跨所价差 %sbps（%s 低 / %s 高），存在搬砖空间",
			symbol, spread.String(), *buy.Venue, *sell.Venue,
		))
	}
	if len(funding) >= 2 {
		var lowestID, highestID string
		var lowest, highest contracts.Decimal
		first := true
		for _, current := range aggregator.venues {
			rate, ok := funding[current.ID()]
			if !ok {
				continue
			}
			if first || rate.Cmp(lowest) < 0 {
				lowestID, lowest = current.ID(), rate
			}
			if first || rate.Cmp(highest) > 0 {
				highestID, highest = current.ID(), rate
			}
			first = false
		}
		gap := highest.Sub(lowest)
		if gap.Cmp(fundingThreshold) >= 0 {
			hints = append(hints, fmt.Sprintf(
				"%s 跨所资金费差 %s（%s 收 / %s 付），可关注费率套利（多低费所/空高费所）",
				symbol, gap.String(), highestID, lowestID,
			))
		}
	} else if len(funding) == 1 {
		for venueID, rate := range funding {
			if rate.Abs().Cmp(fundingThreshold) >= 0 {
				crowding := "空头付费拥挤"
				if rate.IsPositive() {
					crowding = "多头付费拥挤"
				}
				hints = append(hints, fmt.Sprintf(
					"%s %s 资金费 %s（%s），留意反转风险", symbol, venueID, rate.String(), crowding,
				))
			}
		}
	}
	return hints
}

func perpetualSymbol(symbol string) string {
	if strings.Contains(symbol, ":") {
		return symbol
	}
	parts := strings.Split(symbol, "/")
	quote := parts[len(parts)-1]
	return symbol + ":" + quote
}

func stringPointer(value string) *string { return &value }
