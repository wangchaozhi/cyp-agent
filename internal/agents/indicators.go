package agents

import (
	"math"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

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

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func sma(values []float64, length int) *float64 {
	if length <= 0 || len(values) < length {
		return nil
	}
	value := mean(values[len(values)-length:])
	return &value
}

func ema(values []float64, length int) *float64 {
	if length <= 0 || len(values) < length {
		return nil
	}
	k := 2 / float64(length+1)
	value := mean(values[:length])
	for _, current := range values[length:] {
		value = current*k + value*(1-k)
	}
	return &value
}

func relativeStrength(values []float64, length int) *float64 {
	if length <= 0 || len(values) < length+1 {
		return nil
	}
	gains := 0.0
	losses := 0.0
	start := len(values) - length
	for index := start; index < len(values); index++ {
		difference := values[index] - values[index-1]
		if difference > 0 {
			gains += difference
		} else {
			losses -= difference
		}
	}
	if losses == 0 {
		value := 100.0
		return &value
	}
	ratio := (gains / float64(length)) / (losses / float64(length))
	value := 100 - 100/(1+ratio)
	return &value
}

func macdValue(values []float64, fast, slow, signal int) (*float64, *float64) {
	if len(values) < slow+signal {
		return nil, nil
	}
	series := make([]float64, 0, len(values)-slow+1)
	for end := slow; end <= len(values); end++ {
		fastValue := ema(values[:end], fast)
		slowValue := ema(values[:end], slow)
		if fastValue != nil && slowValue != nil {
			series = append(series, *fastValue-*slowValue)
		}
	}
	if len(series) < signal {
		return nil, nil
	}
	signalValue := ema(series, signal)
	if signalValue == nil {
		return nil, nil
	}
	value := series[len(series)-1]
	return &value, signalValue
}

func averageTrueRange(candles []contracts.Candle, length int) *float64 {
	if length <= 0 || len(candles) < length+1 {
		return nil
	}
	ranges := make([]float64, 0, len(candles)-1)
	for index := 1; index < len(candles); index++ {
		high, highErr := candles[index].High.Float64()
		low, lowErr := candles[index].Low.Float64()
		previous, previousErr := candles[index-1].Close.Float64()
		if highErr != nil || lowErr != nil || previousErr != nil {
			return nil
		}
		value := math.Max(high-low, math.Max(math.Abs(high-previous), math.Abs(low-previous)))
		ranges = append(ranges, value)
	}
	value := mean(ranges[len(ranges)-length:])
	return &value
}

func bollinger(values []float64, length int, width float64) (*float64, *float64) {
	if length <= 0 || len(values) < length {
		return nil, nil
	}
	window := values[len(values)-length:]
	middle := mean(window)
	variance := 0.0
	for _, value := range window {
		delta := value - middle
		variance += delta * delta
	}
	deviation := math.Sqrt(variance / float64(len(window)))
	lower := middle - width*deviation
	upper := middle + width*deviation
	return &lower, &upper
}

func indicatorSnapshot(candles []contracts.Candle) indicators {
	values, ok := candleCloses(candles)
	if !ok || len(values) == 0 {
		return indicators{}
	}
	last := values[len(values)-1]
	macd, signal := macdValue(values, 12, 26, 9)
	lower, upper := bollinger(values, 20, 2)
	return indicators{
		lastClose: &last, smaFast: sma(values, 20), smaSlow: sma(values, 50),
		rsi: relativeStrength(values, 14), macd: macd, macdSignal: signal,
		atr: averageTrueRange(candles, 14), bbLower: lower, bbUpper: upper,
	}
}

func simpleReturns(candles []contracts.Candle) []float64 {
	closes, ok := candleCloses(candles)
	if !ok {
		return nil
	}
	returns := make([]float64, 0, len(closes)-1)
	for index := 1; index < len(closes); index++ {
		if closes[index-1] > 0 {
			returns = append(returns, closes[index]/closes[index-1]-1)
		}
	}
	return returns
}

func ewmaVolatility(candles []contracts.Candle, lambda float64) float64 {
	returns := simpleReturns(candles)
	if len(returns) < 2 || lambda < 0 || lambda > 1 || math.IsNaN(lambda) {
		return 0
	}
	variance := returns[0] * returns[0]
	for _, value := range returns[1:] {
		variance = lambda*variance + (1-lambda)*value*value
	}
	return math.Sqrt(math.Max(0, variance))
}
