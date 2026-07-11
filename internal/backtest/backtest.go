// Package backtest provides a deterministic, dependency-free paper backtest.
// It intentionally uses float64 only for research metrics; trading amounts in
// the live runtime use contracts.Decimal.
package backtest

import (
	"errors"
	"fmt"
	"math"
	"math/rand"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

const initialEquity = 10_000.0

// Params is the validated backtest input exposed by the HTTP API.
type Params struct {
	Symbol    string  `json:"symbol"`
	Bars      int     `json:"bars"`
	Window    int     `json:"window"`
	Seed      int64   `json:"seed"`
	Drift     float64 `json:"drift"`
	Vol       float64 `json:"vol"`
	Data      string  `json:"data"`
	Timeframe string  `json:"timeframe"`
}

// Metrics mirrors the dashboard's existing backtest contract.
type Metrics struct {
	InitialEquity float64  `json:"initial_equity"`
	FinalEquity   float64  `json:"final_equity"`
	TotalReturn   float64  `json:"total_return"`
	MaxDrawdown   float64  `json:"max_drawdown"`
	Sharpe        float64  `json:"sharpe"`
	NTrades       int      `json:"n_trades"`
	WinRate       float64  `json:"win_rate"`
	ProfitFactor  *float64 `json:"profit_factor"`
}

// Trade is a completed long paper trade.
type Trade struct {
	Side   string  `json:"side"`
	Entry  float64 `json:"entry"`
	Exit   float64 `json:"exit"`
	PnL    float64 `json:"pnl"`
	BarIn  int     `json:"bar_in"`
	BarOut int     `json:"bar_out"`
}

// Report is JSON-compatible with apps/web/src/shared/api/types.ts.
type Report struct {
	Symbol      string    `json:"symbol"`
	NBars       int       `json:"n_bars"`
	Metrics     Metrics   `json:"metrics"`
	Trades      []Trade   `json:"trades"`
	EquityCurve []float64 `json:"equity_curve"`
	Lessons     []string  `json:"lessons"`
	Params      Params    `json:"params"`
}

// Validate applies the public API bounds used by the HTTP endpoint.
func (p Params) Validate() error {
	if p.Symbol == "" {
		return errors.New("symbol must not be empty")
	}
	if p.Bars < 80 || p.Bars > 5000 {
		return errors.New("bars must be between 80 and 5000")
	}
	if p.Window < 20 || p.Window > 1000 {
		return errors.New("window must be between 20 and 1000")
	}
	if p.Window >= p.Bars {
		return errors.New("window must be smaller than bars")
	}
	if p.Seed < 0 || p.Seed > 1_000_000 {
		return errors.New("seed must be between 0 and 1000000")
	}
	if p.Drift < -0.05 || p.Drift > 0.05 {
		return errors.New("drift must be between -0.05 and 0.05")
	}
	if p.Vol <= 0 || p.Vol > 0.2 {
		return errors.New("vol must be greater than 0 and at most 0.2")
	}
	if p.Data != "synthetic" && p.Data != "cex" {
		return errors.New("data must be synthetic or cex")
	}
	return nil
}

// Run executes a reproducible moving-average paper strategy on synthetic data.
func Run(p Params) (Report, error) {
	return RunWithStrategy(p, DefaultStrategyConfig())
}

// RunWithStrategy executes the same deterministic data path with an explicit
// strategy configuration used by sweep and robust out-of-sample validation.
func RunWithStrategy(p Params, strategy StrategyConfig) (Report, error) {
	if err := p.Validate(); err != nil {
		return Report{}, err
	}
	if p.Data != "synthetic" {
		return Report{}, errors.New("use RunCandles for cex history")
	}

	prices := syntheticPrices(p)
	return runPrices(p, strategy, prices)
}

// RunCandles runs archived or freshly fetched CEX candles through the same
// deterministic engine. It never performs network I/O itself.
func RunCandles(p Params, candles []contracts.Candle, strategy StrategyConfig) (Report, error) {
	if len(candles) < 80 || len(candles) > 5000 {
		return Report{}, errors.New("candles must contain between 80 and 5000 bars")
	}
	p.Bars = len(candles)
	p.Data = "cex"
	if err := p.Validate(); err != nil {
		return Report{}, err
	}
	prices := make([]float64, len(candles))
	for index, candle := range candles {
		value, err := candle.Close.Float64()
		if err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return Report{}, fmt.Errorf("candle %d has invalid close price", index)
		}
		prices[index] = value
	}
	return runPrices(p, strategy, prices)
}

func runPrices(p Params, strategy StrategyConfig, prices []float64) (Report, error) {
	equity := initialEquity
	peak := equity
	maxDrawdown := 0.0
	equityCurve := make([]float64, 0, p.Bars-p.Window+1)
	strategyReturns := make([]float64, 0, p.Bars-p.Window)
	trades := make([]Trade, 0)

	inPosition := false
	entryPrice := 0.0
	entryEquity := 0.0
	entryBar := 0
	previousPrice := prices[p.Window-1]

	for i := p.Window; i < len(prices); i++ {
		mean := average(prices[i-p.Window : i])
		deviation := 0.0
		if mean > 0 {
			deviation = prices[i]/mean - 1
		}
		wantLong := deviation > strategy.EnterThreshold*p.Vol

		periodReturn := 0.0
		if inPosition && previousPrice > 0 {
			periodReturn = prices[i]/previousPrice - 1
			equity *= 1 + periodReturn
		}

		protectiveExit := false
		if inPosition && entryPrice > 0 {
			if strategy.StopVolMultiple > 0 && prices[i] <= entryPrice*(1-strategy.StopVolMultiple*p.Vol) {
				protectiveExit = true
			}
			if strategy.TakeProfitVolMultiple > 0 && prices[i] >= entryPrice*(1+strategy.TakeProfitVolMultiple*p.Vol) {
				protectiveExit = true
			}
		}

		if wantLong && !inPosition {
			inPosition = true
			entryPrice = prices[i]
			entryEquity = equity
			entryBar = i
		} else if (!wantLong || protectiveExit) && inPosition {
			trades = append(trades, Trade{
				Side: "long", Entry: finite(entryPrice), Exit: finite(prices[i]),
				PnL: finite(equity - entryEquity), BarIn: entryBar, BarOut: i,
			})
			inPosition = false
		}

		strategyReturns = append(strategyReturns, finite(periodReturn))
		peak = math.Max(peak, equity)
		if peak > 0 {
			maxDrawdown = math.Max(maxDrawdown, (peak-equity)/peak)
		}
		equityCurve = append(equityCurve, finite(equity))
		previousPrice = prices[i]
	}

	if inPosition {
		last := len(prices) - 1
		trades = append(trades, Trade{
			Side: "long", Entry: finite(entryPrice), Exit: finite(prices[last]),
			PnL: finite(equity - entryEquity), BarIn: entryBar, BarOut: last,
		})
	}

	wins := 0
	grossProfit := 0.0
	grossLoss := 0.0
	for _, trade := range trades {
		if trade.PnL > 0 {
			wins++
			grossProfit += trade.PnL
		} else if trade.PnL < 0 {
			grossLoss += -trade.PnL
		}
	}

	var profitFactor *float64
	if grossLoss > 0 {
		value := roundTo(finite(grossProfit/grossLoss), 4)
		profitFactor = &value
	}
	winRate := 0.0
	if len(trades) > 0 {
		winRate = float64(wins) / float64(len(trades))
	}

	report := Report{
		Symbol: p.Symbol,
		NBars:  p.Bars,
		Metrics: Metrics{
			InitialEquity: initialEquity,
			FinalEquity:   roundTo(finite(equity), 2),
			TotalReturn:   roundTo(finite(equity/initialEquity-1), 4),
			MaxDrawdown:   roundTo(finite(maxDrawdown), 4),
			Sharpe:        roundTo(finite(Sharpe(strategyReturns)), 4),
			NTrades:       len(trades),
			WinRate:       roundTo(finite(winRate), 4),
			ProfitFactor:  profitFactor,
		},
		Trades:      trades,
		EquityCurve: equityCurve,
		Lessons:     []string{"移动均线仅用于迁移验收，不构成交易建议"},
		Params:      p,
	}
	return report, nil
}

func syntheticPrices(p Params) []float64 {
	rng := rand.New(rand.NewSource(p.Seed)) // #nosec G404 -- deterministic fixture generation.
	prices := make([]float64, p.Bars)
	prices[0] = 100
	for i := 1; i < len(prices); i++ {
		step := p.Drift + p.Vol*rng.NormFloat64()
		prices[i] = math.Max(0.00000001, prices[i-1]*math.Exp(step))
	}
	return prices
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func finite(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}
