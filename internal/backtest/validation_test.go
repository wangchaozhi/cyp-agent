package backtest

import (
	"math"
	"math/rand"
	"testing"
)

func TestWalkForwardSplitsAnchoredAndRolling(t *testing.T) {
	if got := WalkForwardSplits(0, 4, 10, true); len(got) != 0 {
		t.Fatalf("empty series must yield no splits, got %v", got)
	}

	anchored := WalkForwardSplits(100, 4, 20, true)
	if len(anchored) != 4 {
		t.Fatalf("anchored split count = %d, want 4", len(anchored))
	}
	for index, split := range anchored {
		if split.TrainStart != 0 {
			t.Fatalf("anchored split %d must start training at 0, got %d", index, split.TrainStart)
		}
		if split.TrainEnd != split.TestStart {
			t.Fatalf("split %d has a train/test gap: %+v", index, split)
		}
		if split.TestStart >= split.TestEnd || split.TestEnd > 100 {
			t.Fatalf("split %d has invalid test window: %+v", index, split)
		}
		if index > 0 && split.TestStart != anchored[index-1].TestEnd {
			t.Fatalf("split %d does not advance contiguously: %+v then %+v", index, anchored[index-1], split)
		}
	}

	rolling := WalkForwardSplits(100, 4, 20, false)
	for index, split := range rolling {
		if split.TrainEnd-split.TrainStart != 20 {
			t.Fatalf("rolling split %d train length = %d, want 20", index, split.TrainEnd-split.TrainStart)
		}
	}

	// Defaults kick in for non-positive counts, and splits never start past n.
	defaulted := WalkForwardSplits(50, 0, 0, true)
	if len(defaulted) == 0 {
		t.Fatal("defaulted parameters must still produce splits")
	}
	tiny := WalkForwardSplits(3, 8, 5, true)
	for _, split := range tiny {
		if split.TestStart >= 3 || split.TestEnd > 3 {
			t.Fatalf("tiny split exceeds series length: %+v", split)
		}
	}
}

func TestPurgedKFoldSplitsEmbargoAndCoverage(t *testing.T) {
	if got := PurgedKFoldSplits(0, 5, 0.1); len(got) != 0 {
		t.Fatalf("empty series must yield no folds, got %v", got)
	}

	const n = 100
	splits := PurgedKFoldSplits(n, 5, 0.05)
	if len(splits) != 5 {
		t.Fatalf("fold count = %d, want 5", len(splits))
	}
	seen := make(map[int]int, n)
	embargoCount := int(float64(n) * 0.05)
	for foldIndex, split := range splits {
		if len(split.Test) == 0 || len(split.Train) == 0 {
			t.Fatalf("fold %d has empty train or test", foldIndex)
		}
		testStart := split.Test[0]
		testEnd := split.Test[len(split.Test)-1] + 1
		for _, index := range split.Test {
			seen[index]++
		}
		for _, index := range split.Train {
			if index >= testStart-embargoCount && index < testEnd+embargoCount {
				t.Fatalf("fold %d leaked embargoed index %d into training (test [%d,%d))",
					foldIndex, index, testStart, testEnd)
			}
		}
	}
	for index := 0; index < n; index++ {
		if seen[index] != 1 {
			t.Fatalf("index %d appears in %d test folds, want exactly 1", index, seen[index])
		}
	}

	// Negative embargo is clamped to zero rather than corrupting the windows.
	noEmbargo := PurgedKFoldSplits(20, 4, -1)
	if len(noEmbargo) != 4 {
		t.Fatalf("negative embargo fold count = %d, want 4", len(noEmbargo))
	}
	if got := len(noEmbargo[0].Train) + len(noEmbargo[0].Test); got != 20 {
		t.Fatalf("with zero embargo train+test must cover the series, got %d", got)
	}
}

func TestProbabilityBacktestOverfitSeparatesSkillFromNoise(t *testing.T) {
	if got := ProbabilityBacktestOverfit(nil, 8, nil); got != 0 {
		t.Fatalf("PBO with no strategies = %v, want 0", got)
	}
	if got := ProbabilityBacktestOverfit([][]float64{{0.1, 0.2}}, 8, nil); got != 0 {
		t.Fatalf("PBO with one strategy = %v, want 0", got)
	}

	generator := rand.New(rand.NewSource(7))
	const length = 240
	skilled := make([]float64, length)
	for index := range skilled {
		skilled[index] = 0.004 + generator.NormFloat64()*0.002
	}
	noise := make([][]float64, 0, 9)
	for strategy := 0; strategy < 9; strategy++ {
		returns := make([]float64, length)
		for index := range returns {
			returns[index] = generator.NormFloat64() * 0.01
		}
		noise = append(noise, returns)
	}

	withSkill := ProbabilityBacktestOverfit(append([][]float64{skilled}, noise...), 8, nil)
	if withSkill > 0.2 {
		t.Fatalf("PBO with a persistently skilled strategy = %v, want <= 0.2", withSkill)
	}
	pureNoise := ProbabilityBacktestOverfit(noise, 8, nil)
	if pureNoise <= withSkill {
		t.Fatalf("pure-noise PBO (%v) must exceed skilled PBO (%v)", pureNoise, withSkill)
	}
	if pureNoise < 0 || pureNoise > 1 {
		t.Fatalf("PBO must be a probability, got %v", pureNoise)
	}
}

func TestProbabilityBacktestOverfitDegenerateSegments(t *testing.T) {
	returns := [][]float64{
		{0.01, 0.02, -0.01, 0.03},
		{-0.02, 0.01, 0.02, -0.03},
	}
	// Odd and oversized segment counts are normalized instead of panicking.
	for _, segments := range []int{1, 3, 7, 100} {
		got := ProbabilityBacktestOverfit(returns, segments, nil)
		if math.IsNaN(got) || got < 0 || got > 1 {
			t.Fatalf("segments=%d produced invalid PBO %v", segments, got)
		}
	}
	// Zero-length inner series short-circuits to 0.
	if got := ProbabilityBacktestOverfit([][]float64{{}, {0.1}}, 4, nil); got != 0 {
		t.Fatalf("PBO with empty strategy returns = %v, want 0", got)
	}
}

func TestIndexCombinations(t *testing.T) {
	combos := indexCombinations(4, 2)
	if len(combos) != 6 {
		t.Fatalf("C(4,2) = %d, want 6", len(combos))
	}
	unique := make(map[[2]int]struct{}, len(combos))
	for _, combo := range combos {
		if len(combo) != 2 || combo[0] >= combo[1] {
			t.Fatalf("combination %v is not strictly increasing", combo)
		}
		unique[[2]int{combo[0], combo[1]}] = struct{}{}
	}
	if len(unique) != 6 {
		t.Fatalf("combinations are not unique: %v", combos)
	}
}
