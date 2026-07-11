package data

import (
	"math"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func SimpleReturns(candles []contracts.Candle) []float64 {
	closes := closeValues(candles)
	returns := make([]float64, 0, max(0, len(closes)-1))
	for index := 1; index < len(closes); index++ {
		if closes[index-1] > 0 {
			returns = append(returns, closes[index]/closes[index-1]-1)
		}
	}
	return returns
}

// EWMAVolatility computes per-period RiskMetrics EWMA volatility. Lambda
// defaults to 0.94 when omitted.
func EWMAVolatility(returns []float64, lambda ...float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	decay := 0.94
	if len(lambda) > 0 {
		decay = lambda[0]
	}
	variance := returns[0] * returns[0]
	for _, value := range returns[1:] {
		variance = decay*variance + (1-decay)*value*value
	}
	if variance <= 0 {
		return 0
	}
	return math.Sqrt(variance)
}

func RealizedVolatility(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	return populationStdDev(returns)
}

func EWMAVolFromCandles(candles []contracts.Candle, lambda ...float64) float64 {
	return EWMAVolatility(SimpleReturns(candles), lambda...)
}
