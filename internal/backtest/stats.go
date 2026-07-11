package backtest

import "math"

const eulerMascheroni = 0.5772156649015329

func NormCDF(value float64) float64 {
	return 0.5 * (1 + math.Erf(value/math.Sqrt2))
}

// NormPPF is Acklam's rational approximation of the normal quantile.
func NormPPF(probability float64) float64 {
	if probability <= 0 {
		return math.Inf(-1)
	}
	if probability >= 1 {
		return math.Inf(1)
	}
	a := [...]float64{-39.69683028665376, 220.9460984245205, -275.9285104469687, 138.3577518672690, -30.66479806614716, 2.506628277459239}
	b := [...]float64{-54.47609879822406, 161.5858368580409, -155.6989798598866, 66.80131188771972, -13.28068155288572}
	c := [...]float64{-0.007784894002430293, -0.3223964580411365, -2.400758277161838, -2.549732539343734, 4.374664141464968, 2.938163982698783}
	d := [...]float64{0.007784695709041462, 0.3224671290700398, 2.445134137142996, 3.754408661907416}
	const low = 0.02425
	const high = 1 - low
	if probability < low {
		q := math.Sqrt(-2 * math.Log(probability))
		return (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	}
	if probability > high {
		q := math.Sqrt(-2 * math.Log(1-probability))
		return -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	}
	q := probability - 0.5
	r := q * q
	return (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q /
		(((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1)
}

type returnMoments struct {
	sharpe   float64
	skew     float64
	kurtosis float64
	count    int
}

func moments(returns []float64) returnMoments {
	mean := average(returns)
	variance := populationVariance(returns, mean)
	deviation := math.Sqrt(variance)
	if deviation == 0 {
		return returnMoments{sharpe: 0, skew: 0, kurtosis: 3, count: len(returns)}
	}
	skew, kurtosis := 0.0, 0.0
	for _, value := range returns {
		standardized := (value - mean) / deviation
		skew += math.Pow(standardized, 3)
		kurtosis += math.Pow(standardized, 4)
	}
	skew /= float64(len(returns))
	kurtosis /= float64(len(returns))
	return returnMoments{
		sharpe: mean / deviation, skew: skew, kurtosis: kurtosis, count: len(returns),
	}
}

func Sharpe(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	return moments(returns).sharpe
}

func ProbabilisticSharpe(returns []float64, benchmark float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	value := moments(returns)
	denominator := math.Sqrt(math.Max(1e-12,
		1-value.skew*value.sharpe+(value.kurtosis-1)/4*value.sharpe*value.sharpe))
	z := (value.sharpe - benchmark) * math.Sqrt(float64(value.count-1)) / denominator
	return NormCDF(z)
}

func ExpectedMaxSharpe(trialSharpes []float64) float64 {
	if len(trialSharpes) < 2 {
		return 0
	}
	deviation := math.Sqrt(populationVariance(trialSharpes, average(trialSharpes)))
	if deviation == 0 {
		return 0
	}
	n := float64(len(trialSharpes))
	z1 := NormPPF(1 - 1/n)
	z2 := NormPPF(1 - 1/(n*math.E))
	return deviation * ((1-eulerMascheroni)*z1 + eulerMascheroni*z2)
}

func DeflatedSharpe(returns, trialSharpes []float64) float64 {
	return ProbabilisticSharpe(returns, ExpectedMaxSharpe(trialSharpes))
}

func MinTrackRecordLength(returns []float64, benchmark, targetProbability float64) float64 {
	if len(returns) < 2 {
		return math.Inf(1)
	}
	value := moments(returns)
	if value.sharpe <= benchmark {
		return math.Inf(1)
	}
	denominator := 1 - value.skew*value.sharpe + (value.kurtosis-1)/4*value.sharpe*value.sharpe
	ratio := NormPPF(targetProbability) / (value.sharpe - benchmark)
	return 1 + denominator*ratio*ratio
}

func populationVariance(values []float64, mean float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		delta := value - mean
		total += delta * delta
	}
	return total / float64(len(values))
}
