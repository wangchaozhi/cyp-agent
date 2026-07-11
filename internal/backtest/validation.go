package backtest

import (
	"math"
	"sort"
)

type WalkForwardSplit struct {
	TrainStart int
	TrainEnd   int
	TestStart  int
	TestEnd    int
}

func WalkForwardSplits(n, splitCount, minimumTrain int, anchored bool) []WalkForwardSplit {
	if n <= 0 {
		return []WalkForwardSplit{}
	}
	if splitCount <= 0 {
		splitCount = 4
	}
	fold := n / (splitCount + 1)
	if fold < 1 {
		fold = 1
	}
	if minimumTrain <= 0 {
		minimumTrain = fold
	}
	result := make([]WalkForwardSplit, 0, splitCount)
	for index := 0; index < splitCount; index++ {
		testStart := minimumTrain + index*fold
		if testStart >= n {
			break
		}
		testEnd := minInt(n, testStart+fold)
		trainStart := 0
		if !anchored {
			trainStart = maxInt(0, testStart-minimumTrain)
		}
		result = append(result, WalkForwardSplit{
			TrainStart: trainStart, TrainEnd: testStart,
			TestStart: testStart, TestEnd: testEnd,
		})
	}
	return result
}

type PurgedSplit struct {
	Train []int
	Test  []int
}

func PurgedKFoldSplits(n, folds int, embargo float64) []PurgedSplit {
	if n <= 0 {
		return []PurgedSplit{}
	}
	if folds <= 0 {
		folds = 5
	}
	fold := n / folds
	if fold < 1 {
		fold = 1
	}
	if embargo < 0 {
		embargo = 0
	}
	embargoCount := int(float64(n) * embargo)
	result := make([]PurgedSplit, 0, folds)
	for index := 0; index < folds; index++ {
		testStart := index * fold
		if testStart >= n {
			break
		}
		testEnd := (index + 1) * fold
		if index == folds-1 || testEnd > n {
			testEnd = n
		}
		test := make([]int, 0, testEnd-testStart)
		for item := testStart; item < testEnd; item++ {
			test = append(test, item)
		}
		purgeLow := maxInt(0, testStart-embargoCount)
		purgeHigh := minInt(n, testEnd+embargoCount)
		train := make([]int, 0, n-(purgeHigh-purgeLow))
		for item := 0; item < n; item++ {
			if item < purgeLow || item >= purgeHigh {
				train = append(train, item)
			}
		}
		result = append(result, PurgedSplit{Train: train, Test: test})
	}
	return result
}

type PerformanceMetric func([]float64) float64

// ProbabilityBacktestOverfit implements combinatorially purged validation.
func ProbabilityBacktestOverfit(strategyReturns [][]float64, segments int, metric PerformanceMetric) float64 {
	if len(strategyReturns) < 2 {
		return 0
	}
	length := len(strategyReturns[0])
	for _, returns := range strategyReturns {
		if len(returns) < length {
			length = len(returns)
		}
	}
	if length == 0 {
		return 0
	}
	if segments < 2 {
		segments = 2
	}
	if segments%2 != 0 {
		segments--
	}
	if segments > length {
		segments = length
		if segments%2 != 0 {
			segments--
		}
		if segments < 2 {
			return 0
		}
	}
	if metric == nil {
		metric = Sharpe
	}
	bounds := make([]int, segments+1)
	for index := range bounds {
		bounds[index] = int(math.Round(float64(index*length) / float64(segments)))
	}
	combinations := indexCombinations(segments, segments/2)
	overfit := 0
	for _, inSampleGroups := range combinations {
		selected := make(map[int]struct{}, len(inSampleGroups))
		for _, group := range inSampleGroups {
			selected[group] = struct{}{}
		}
		outSampleGroups := make([]int, 0, segments/2)
		for group := 0; group < segments; group++ {
			if _, ok := selected[group]; !ok {
				outSampleGroups = append(outSampleGroups, group)
			}
		}
		inPerformance := make([]float64, len(strategyReturns))
		outPerformance := make([]float64, len(strategyReturns))
		for strategy := range strategyReturns {
			inPerformance[strategy] = metric(groupReturns(strategyReturns[strategy][:length], inSampleGroups, bounds))
			outPerformance[strategy] = metric(groupReturns(strategyReturns[strategy][:length], outSampleGroups, bounds))
		}
		best := 0
		for index := 1; index < len(inPerformance); index++ {
			if inPerformance[index] > inPerformance[best] {
				best = index
			}
		}
		ranking := make([]int, len(outPerformance))
		for index := range ranking {
			ranking[index] = index
		}
		sort.SliceStable(ranking, func(i, j int) bool { return outPerformance[ranking[i]] < outPerformance[ranking[j]] })
		rank := 0
		for index, strategy := range ranking {
			if strategy == best {
				rank = index
				break
			}
		}
		weight := float64(rank+1) / float64(len(strategyReturns)+1)
		lambda := math.Log(weight / (1 - weight))
		if lambda <= 0 {
			overfit++
		}
	}
	if len(combinations) == 0 {
		return 0
	}
	return float64(overfit) / float64(len(combinations))
}

func groupReturns(returns []float64, groups, bounds []int) []float64 {
	result := make([]float64, 0, len(returns)/2)
	for _, group := range groups {
		result = append(result, returns[bounds[group]:bounds[group+1]]...)
	}
	return result
}

func indexCombinations(total, choose int) [][]int {
	result := make([][]int, 0)
	current := make([]int, 0, choose)
	var visit func(int)
	visit = func(start int) {
		if len(current) == choose {
			copyValue := append([]int{}, current...)
			result = append(result, copyValue)
			return
		}
		for value := start; value <= total-(choose-len(current)); value++ {
			current = append(current, value)
			visit(value + 1)
			current = current[:len(current)-1]
		}
	}
	visit(0)
	return result
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
