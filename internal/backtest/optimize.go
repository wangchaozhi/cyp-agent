package backtest

import (
	"errors"
	"math"
	"sort"
)

type StrategyConfig struct {
	EnterThreshold        float64 `json:"enter_threshold"`
	StopVolMultiple       float64 `json:"k_stop"`
	TakeProfitVolMultiple float64 `json:"k_tp"`
}

func DefaultStrategyConfig() StrategyConfig { return StrategyConfig{} }

// Grid produces a Cartesian product. Empty dimensions use the default value.
func Grid(enterThresholds, stopMultiples, takeProfitMultiples []float64) []StrategyConfig {
	if len(enterThresholds) == 0 {
		enterThresholds = []float64{0}
	}
	if len(stopMultiples) == 0 {
		stopMultiples = []float64{0}
	}
	if len(takeProfitMultiples) == 0 {
		takeProfitMultiples = []float64{0}
	}
	result := make([]StrategyConfig, 0, len(enterThresholds)*len(stopMultiples)*len(takeProfitMultiples))
	for _, threshold := range enterThresholds {
		for _, stop := range stopMultiples {
			for _, takeProfit := range takeProfitMultiples {
				result = append(result, StrategyConfig{
					EnterThreshold: threshold, StopVolMultiple: stop,
					TakeProfitVolMultiple: takeProfit,
				})
			}
		}
	}
	return result
}

type Objective func(Metrics) float64

func DefaultObjective(metrics Metrics) float64 {
	return roundTo(metrics.TotalReturn-metrics.MaxDrawdown, 4)
}

type SweepResult struct {
	Config  StrategyConfig `json:"config"`
	Metrics Metrics        `json:"metrics"`
	Score   float64        `json:"score"`
	Lessons []string       `json:"lessons"`
}

func Sweep(params Params, configs []StrategyConfig, objective Objective) ([]SweepResult, error) {
	if len(configs) == 0 {
		return nil, errors.New("at least one strategy config is required")
	}
	if objective == nil {
		objective = DefaultObjective
	}
	results := make([]SweepResult, 0, len(configs))
	for _, strategy := range configs {
		report, err := RunWithStrategy(params, strategy)
		if err != nil {
			return nil, err
		}
		lessons := append([]string{}, report.Lessons...)
		if len(lessons) > 5 {
			lessons = lessons[len(lessons)-5:]
		}
		results = append(results, SweepResult{
			Config: strategy, Metrics: report.Metrics,
			Score: objective(report.Metrics), Lessons: lessons,
		})
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	return results, nil
}

type RobustResult struct {
	BestConfig     StrategyConfig `json:"best_config"`
	InSample       Metrics        `json:"is_metrics"`
	OutOfSample    Metrics        `json:"oos_metrics"`
	PBO            float64        `json:"pbo"`
	DeflatedSharpe float64        `json:"deflated_sharpe"`
	Verdict        string         `json:"verdict"`
}

func RobustSweep(
	params Params,
	configs []StrategyConfig,
	outOfSampleFraction float64,
	pboMaximum, dsrMinimum float64,
) (RobustResult, error) {
	if len(configs) == 0 {
		return RobustResult{}, errors.New("at least one strategy config is required")
	}
	if outOfSampleFraction <= 0 || outOfSampleFraction >= 1 {
		outOfSampleFraction = 0.3
	}
	split := int(float64(params.Bars) * (1 - outOfSampleFraction))
	if split < params.Window+2 {
		split = params.Window + 2
	}
	if split >= params.Bars {
		return RobustResult{}, errors.New("not enough bars for out-of-sample validation")
	}
	inParams := params
	inParams.Bars = split
	inReports := make([]Report, len(configs))
	scores := make([]float64, len(configs))
	returnMatrix := make([][]float64, len(configs))
	best := 0
	for index, strategy := range configs {
		report, err := RunWithStrategy(inParams, strategy)
		if err != nil {
			return RobustResult{}, err
		}
		inReports[index] = report
		scores[index] = DefaultObjective(report.Metrics)
		returnMatrix[index] = BarReturns(report.EquityCurve)
		if scores[index] > scores[best] {
			best = index
		}
	}
	usable := len(returnMatrix[0])
	for _, returns := range returnMatrix {
		if len(returns) < usable {
			usable = len(returns)
		}
	}
	matrix := make([][]float64, 0, len(returnMatrix))
	trialSharpes := make([]float64, 0, len(returnMatrix))
	if usable > 1 {
		for _, returns := range returnMatrix {
			trimmed := append([]float64{}, returns[:usable]...)
			matrix = append(matrix, trimmed)
			trialSharpes = append(trialSharpes, Sharpe(trimmed))
		}
	}
	pboValue := 0.0
	if usable >= 12 && len(matrix) >= 2 {
		pboValue = ProbabilityBacktestOverfit(matrix, 6, nil)
	}
	if len(trialSharpes) == 0 {
		trialSharpes = []float64{0}
	}

	outParams := params
	outParams.Bars = params.Bars - split + params.Window
	outParams.Seed = params.Seed + int64(split) + 1
	outReport, err := RunWithStrategy(outParams, configs[best])
	if err != nil {
		return RobustResult{}, err
	}
	outReturns := BarReturns(outReport.EquityCurve)
	dsr := 0.0
	if len(outReturns) > 1 {
		dsr = DeflatedSharpe(outReturns, trialSharpes)
	}
	verdict := "REJECT(疑似过拟合)"
	if pboValue <= pboMaximum && outReport.Metrics.TotalReturn > 0 && dsr >= dsrMinimum {
		verdict = "PASS"
	}
	return RobustResult{
		BestConfig: configs[best], InSample: inReports[best].Metrics,
		OutOfSample: outReport.Metrics, PBO: roundTo(pboValue, 4),
		DeflatedSharpe: roundTo(dsr, 4), Verdict: verdict,
	}, nil
}

func roundTo(value float64, precision int) float64 {
	power := math.Pow10(precision)
	return math.Round(value*power) / power
}

func BarReturns(equityCurve []float64) []float64 {
	result := make([]float64, 0, len(equityCurve)-1)
	for index := 1; index < len(equityCurve); index++ {
		if equityCurve[index-1] > 0 {
			result = append(result, equityCurve[index]/equityCurve[index-1]-1)
		}
	}
	return result
}
