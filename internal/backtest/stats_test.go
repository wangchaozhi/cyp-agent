package backtest

import (
	"math"
	"math/rand"
	"testing"
)

func TestNormalFunctionsAndSharpeStatistics(t *testing.T) {
	if math.Abs(NormCDF(0)-0.5) > 1e-9 || math.Abs(NormCDF(1.96)-0.975) > 1e-3 {
		t.Fatal("NormCDF known points failed")
	}
	for _, value := range []float64{-2, -0.5, 0.3, 1.5} {
		if math.Abs(NormPPF(NormCDF(value))-value) > 1e-4 {
			t.Fatalf("NormPPF inverse failed for %v", value)
		}
	}
	returns := []float64{0.01, -0.004, 0.013, 0.009, 0.011, -0.002, 0.014, 0.007}
	raw := ProbabilisticSharpe(returns, 0)
	deflated := DeflatedSharpe(returns, []float64{Sharpe(returns), 0.05, 0.1, 0.08, 0.12, 0.03, 0.09})
	if deflated >= raw || deflated < 0 || deflated > 1 {
		t.Fatalf("unexpected deflated Sharpe: raw=%v deflated=%v", raw, deflated)
	}
	if !math.IsInf(MinTrackRecordLength([]float64{-0.01, -0.02, -0.005}, 0, 0.95), 1) {
		t.Fatal("losing track record should require infinite length")
	}
}

func TestTimeSeriesSplitsAndPBO(t *testing.T) {
	walk := WalkForwardSplits(100, 4, 0, true)
	if len(walk) != 4 {
		t.Fatalf("walk splits = %d", len(walk))
	}
	for _, split := range walk {
		if split.TrainStart != 0 || split.TrainEnd != split.TestStart || split.TestStart >= split.TestEnd {
			t.Fatalf("invalid walk split: %#v", split)
		}
	}
	purged := PurgedKFoldSplits(100, 5, 0.05)
	for _, index := range purged[2].Train {
		if index >= 35 && index < 65 {
			t.Fatalf("embargo leaked index %d", index)
		}
	}
	mean := func(values []float64) float64 { return average(values) }
	if value := ProbabilityBacktestOverfit([][]float64{
		repeatFloat(0.02, 60), repeatFloat(0.001, 60), repeatFloat(-0.01, 60),
	}, 4, mean); value != 0 {
		t.Fatalf("dominant strategy PBO = %v", value)
	}
	rng := rand.New(rand.NewSource(42))
	strategies := make([][]float64, 6)
	for i := range strategies {
		strategies[i] = make([]float64, 120)
		for j := range strategies[i] {
			strategies[i][j] = rng.NormFloat64() * 0.01
		}
	}
	value := ProbabilityBacktestOverfit(strategies, 6, nil)
	if value < 0 || value > 1 {
		t.Fatalf("PBO = %v", value)
	}
}

func repeatFloat(value float64, count int) []float64 {
	result := make([]float64, count)
	for index := range result {
		result[index] = value
	}
	return result
}
