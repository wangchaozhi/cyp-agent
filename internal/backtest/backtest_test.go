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
