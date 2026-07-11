package backtest

import "testing"

func TestGridSweepAndRobustSweep(t *testing.T) {
	configs := Grid([]float64{0.08, 0.15}, []float64{2, 3}, nil)
	if len(configs) != 4 {
		t.Fatalf("grid size = %d, want 4", len(configs))
	}
	params := Params{
		Symbol: "BTC/USDT", Bars: 260, Window: 60, Seed: 7,
		Drift: 0.002, Vol: 0.01, Data: "synthetic", Timeframe: "1h",
	}
	results, err := Sweep(params, configs, nil)
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(results) != len(configs) {
		t.Fatalf("results size = %d", len(results))
	}
	for index := 1; index < len(results); index++ {
		if results[index-1].Score < results[index].Score {
			t.Fatalf("results not sorted: %#v", results)
		}
	}
	robust, err := RobustSweep(params, configs, 0.3, 0.5, 0.5)
	if err != nil {
		t.Fatalf("RobustSweep() error = %v", err)
	}
	if robust.PBO < 0 || robust.PBO > 1 || robust.DeflatedSharpe < 0 || robust.DeflatedSharpe > 1 {
		t.Fatalf("invalid robust stats: %#v", robust)
	}
	if robust.Verdict != "PASS" && robust.Verdict != "REJECT(疑似过拟合)" {
		t.Fatalf("invalid verdict: %s", robust.Verdict)
	}
}

func TestDefaultObjective(t *testing.T) {
	if got := DefaultObjective(Metrics{TotalReturn: 0.10, MaxDrawdown: 0.03}); got != 0.07 {
		t.Fatalf("objective = %v", got)
	}
}
