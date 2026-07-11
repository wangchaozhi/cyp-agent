package backtest

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRunIsDeterministicAndJSONSafe(t *testing.T) {
	p := Params{
		Symbol: "BTC/USDT", Bars: 120, Window: 30, Seed: 11,
		Drift: 0.001, Vol: 0.01, Data: "synthetic", Timeframe: "1h",
	}
	first, err := Run(p)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	second, err := Run(p)
	if err != nil {
		t.Fatalf("Run() second error = %v", err)
	}
	if !reflect.DeepEqual(first.Metrics, second.Metrics) {
		t.Fatalf("metrics are not deterministic: %#v != %#v", first.Metrics, second.Metrics)
	}
	if first.NBars != 120 || first.Params.Window != 30 || len(first.EquityCurve) == 0 {
		t.Fatalf("unexpected report: %#v", first)
	}
	if _, err := json.Marshal(first); err != nil {
		t.Fatalf("report is not JSON-safe: %v", err)
	}
}

func TestValidateMatchesHTTPBounds(t *testing.T) {
	base := Params{
		Symbol: "BTC/USDT", Bars: 120, Window: 30, Seed: 7,
		Drift: 0.001, Vol: 0.01, Data: "synthetic", Timeframe: "1h",
	}
	tests := []struct {
		name   string
		mutate func(*Params)
	}{
		{"bars too small", func(p *Params) { p.Bars = 79 }},
		{"window not smaller", func(p *Params) { p.Window = p.Bars }},
		{"negative seed", func(p *Params) { p.Seed = -1 }},
		{"zero vol", func(p *Params) { p.Vol = 0 }},
		{"unknown data", func(p *Params) { p.Data = "file" }},
		{"fee too high", func(p *Params) { p.FeeRate = 0.02 }},
		{"negative slippage", func(p *Params) { p.SlippageBPS = -1 }},
		{"funding too high", func(p *Params) { p.FundingRate = 0.02 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := base
			tt.mutate(&p)
			if err := p.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}

func TestExecutionCostsReduceBacktestEquity(t *testing.T) {
	base := Params{
		Symbol: "BTC/USDT", Bars: 180, Window: 30, Seed: 11,
		Drift: 0.002, Vol: 0.01, Data: "synthetic", Timeframe: "1h",
	}
	withoutCosts, err := Run(base)
	if err != nil {
		t.Fatal(err)
	}
	withCostsParams := base
	withCostsParams.FeeRate = 0.0004
	withCostsParams.SlippageBPS = 5
	withCostsParams.SpreadBPS = 2
	withCostsParams.FundingRate = 0.0001
	withCosts, err := Run(withCostsParams)
	if err != nil {
		t.Fatal(err)
	}
	if len(withCosts.Trades) == 0 || withCosts.Metrics.TotalCosts <= 0 {
		t.Fatalf("expected costed trades, got %+v", withCosts.Metrics)
	}
	if withCosts.Metrics.FinalEquity >= withoutCosts.Metrics.FinalEquity {
		t.Fatalf("costs did not reduce equity: costed=%f raw=%f", withCosts.Metrics.FinalEquity, withoutCosts.Metrics.FinalEquity)
	}
	if withCosts.Trades[0].Costs <= 0 {
		t.Fatalf("trade omitted execution costs: %+v", withCosts.Trades[0])
	}
}
