// Package data implements deterministic market-data sources and dependency-
// free technical statistics. Financial amounts stay Decimal at boundaries;
// indicators intentionally use float64 because they are statistical signals,
package data

import (
	"math"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func populationStdDev(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	average := mean(values)
	var sum float64
	for _, value := range values {
		delta := value - average
		sum += delta * delta
	}
	return math.Sqrt(sum / float64(len(values)))
}

// SMA returns the simple moving average over the latest period.
func SMA(values []float64, period int) (float64, bool) {
	if period <= 0 || len(values) < period {
		return 0, false
	}
	return mean(values[len(values)-period:]), true
}

// EMA uses the first period's SMA as its deterministic seed.
func EMA(values []float64, period int) (float64, bool) {
	if period <= 0 || len(values) < period {
		return 0, false
	}
	weight := 2 / float64(period+1)
	value := mean(values[:period])
	for _, current := range values[period:] {
		value = current*weight + value*(1-weight)
	}
	return value, true
}

// RSI returns the latest unsmoothed n-period relative-strength index.
func RSI(values []float64, period int) (float64, bool) {
	if period <= 0 || len(values) < period+1 {
		return 0, false
	}
	var gains, losses float64
	start := len(values) - period
	for index := start; index < len(values); index++ {
		difference := values[index] - values[index-1]
		if difference > 0 {
			gains += difference
		} else {
			losses -= difference
		}
	}
	averageLoss := losses / float64(period)
	if averageLoss == 0 {
		return 100, true
	}
	relativeStrength := (gains / float64(period)) / averageLoss
	return 100 - 100/(1+relativeStrength), true
}

type MACDValue struct {
	Line   float64
	Signal float64
}

// MACD returns the latest MACD and signal lines.
func MACD(values []float64, fast, slow, signal int) (MACDValue, bool) {
	if fast <= 0 || slow <= 0 || signal <= 0 || len(values) < slow+signal {
		return MACDValue{}, false
	}
	series := make([]float64, 0, len(values)-slow+1)
	for end := slow; end <= len(values); end++ {
		fastEMA, fastOK := EMA(values[:end], fast)
		slowEMA, slowOK := EMA(values[:end], slow)
		if fastOK && slowOK {
			series = append(series, fastEMA-slowEMA)
		}
	}
	if len(series) < signal {
		return MACDValue{}, false
	}
	signalLine, ok := EMA(series, signal)
	if !ok {
		return MACDValue{}, false
	}
	return MACDValue{Line: series[len(series)-1], Signal: signalLine}, true
}

// ATR returns the arithmetic mean of the latest true ranges.
func ATR(candles []contracts.Candle, period int) (float64, bool) {
	if period <= 0 || len(candles) < period+1 {
		return 0, false
	}
	ranges := make([]float64, 0, len(candles)-1)
	for index := 1; index < len(candles); index++ {
		high, _ := candles[index].High.Float64()
		low, _ := candles[index].Low.Float64()
		previousClose, _ := candles[index-1].Close.Float64()
		value := math.Max(high-low, math.Max(math.Abs(high-previousClose), math.Abs(low-previousClose)))
		ranges = append(ranges, value)
	}
	return mean(ranges[len(ranges)-period:]), true
}

type BollingerBands struct {
	Lower float64
	Mid   float64
	Upper float64
}

func Bollinger(values []float64, period int, deviations float64) (BollingerBands, bool) {
	if period <= 0 || len(values) < period {
		return BollingerBands{}, false
	}
	window := values[len(values)-period:]
	mid := mean(window)
	width := deviations * populationStdDev(window)
	return BollingerBands{Lower: mid - width, Mid: mid, Upper: mid + width}, true
}

func closeValues(candles []contracts.Candle) []float64 {
	values := make([]float64, 0, len(candles))
	for _, candle := range candles {
		value, err := candle.Close.Float64()
		if err == nil {
			values = append(values, value)
		}
	}
	return values
}

func floatPointer(value float64) *float64 { return &value }

// IndicatorSnapshot returns the dashboard/agent-compatible latest values;
// unavailable indicators are explicit nil values.
func IndicatorSnapshot(candles []contracts.Candle) map[string]*float64 {
	values := closeValues(candles)
	result := map[string]*float64{
		"last_close":  nil,
		"sma_fast":    nil,
		"sma_slow":    nil,
		"ema_fast":    nil,
		"rsi":         nil,
		"macd":        nil,
		"macd_signal": nil,
		"atr":         nil,
		"bb_lower":    nil,
		"bb_upper":    nil,
	}
	if len(values) > 0 {
		result["last_close"] = floatPointer(values[len(values)-1])
	}
	if value, ok := SMA(values, 20); ok {
		result["sma_fast"] = floatPointer(value)
	}
	if value, ok := SMA(values, 50); ok {
		result["sma_slow"] = floatPointer(value)
	}
	if value, ok := EMA(values, 12); ok {
		result["ema_fast"] = floatPointer(value)
	}
	if value, ok := RSI(values, 14); ok {
		result["rsi"] = floatPointer(value)
	}
	if value, ok := MACD(values, 12, 26, 9); ok {
		result["macd"] = floatPointer(value.Line)
		result["macd_signal"] = floatPointer(value.Signal)
	}
	if value, ok := ATR(candles, 14); ok {
		result["atr"] = floatPointer(value)
	}
	if value, ok := Bollinger(values, 20, 2); ok {
		result["bb_lower"] = floatPointer(value.Lower)
		result["bb_upper"] = floatPointer(value.Upper)
	}
	return result
}
