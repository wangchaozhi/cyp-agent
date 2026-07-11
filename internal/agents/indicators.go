package agents

import (
	"math"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/data"
)

// indicators adapts the shared data-package indicator math into the nullable
// shape that analysts and the strategist consume. Agents are stricter than
// the data package: any non-convertible close degrades the whole snapshot
// instead of silently skipping candles.
type indicators struct {
	lastClose  *float64
	smaFast    *float64
	smaSlow    *float64
	rsi        *float64
	macd       *float64
	macdSignal *float64
	atr        *float64
	bbLower    *float64
	bbUpper    *float64
}

func candleCloses(candles []contracts.Candle) ([]float64, bool) {
	values := make([]float64, 0, len(candles))
	for _, candle := range candles {
		value, err := candle.Close.Float64()
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
}

func floatPtr(value float64) *float64 { return &value }

func indicatorSnapshot(candles []contracts.Candle) indicators {
	values, ok := candleCloses(candles)
	if !ok || len(values) == 0 {
		return indicators{}
	}
	result := indicators{lastClose: floatPtr(values[len(values)-1])}
	if value, ok := data.SMA(values, 20); ok {
		result.smaFast = floatPtr(value)
	}
	if value, ok := data.SMA(values, 50); ok {
		result.smaSlow = floatPtr(value)
	}
	if value, ok := data.RSI(values, 14); ok {
		result.rsi = floatPtr(value)
	}
	if value, ok := data.MACD(values, 12, 26, 9); ok {
		result.macd = floatPtr(value.Line)
		result.macdSignal = floatPtr(value.Signal)
	}
	if value, ok := data.ATR(candles, 14); ok {
		result.atr = floatPtr(value)
	}
	if value, ok := data.Bollinger(values, 20, 2); ok {
		result.bbLower = floatPtr(value.Lower)
		result.bbUpper = floatPtr(value.Upper)
	}
	return result
}

func simpleReturns(candles []contracts.Candle) []float64 {
	closes, ok := candleCloses(candles)
	if !ok {
		return nil
	}
	returns := make([]float64, 0, max(0, len(closes)-1))
	for index := 1; index < len(closes); index++ {
		if closes[index-1] > 0 {
			returns = append(returns, closes[index]/closes[index-1]-1)
		}
	}
	return returns
}

func ewmaVolatility(candles []contracts.Candle, lambda float64) float64 {
	if lambda < 0 || lambda > 1 || math.IsNaN(lambda) {
		return 0
	}
	return data.EWMAVolatility(simpleReturns(candles), lambda)
}
