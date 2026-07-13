package runtime

import (
	"fmt"
	"math"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type ExitObservation struct {
	Position contracts.Position
	Mark     contracts.Decimal
	StopLoss contracts.Decimal
	OpenedAt time.Time
	Now      time.Time
}

type ExitDecision struct {
	Trigger       bool          `json:"trigger"`
	Reason        string        `json:"reason"`
	CurrentR      float64       `json:"current_r"`
	PeakR         float64       `json:"peak_r"`
	VolatilityR   float64       `json:"volatility_r"`
	TrailFloorR   float64       `json:"trail_floor_r"`
	Samples       int           `json:"samples"`
	Confirmations int           `json:"confirmations"`
	Holding       time.Duration `json:"holding"`
}

type exitSeries struct {
	previousMark  float64
	variance      float64
	peakR         float64
	samples       int
	confirmations int
	firstSeen     time.Time
}

// ExitModel applies an EWMA volatility-adjusted trailing threshold and a time
// stop. All distances are normalized by the original stop distance (R), so the
// same policy is comparable across BTC, ETH, and altcoin price scales.
type ExitModel struct {
	series map[string]*exitSeries
}

func NewExitModel() *ExitModel { return &ExitModel{series: make(map[string]*exitSeries)} }

func (model *ExitModel) Reset() { model.series = make(map[string]*exitSeries) }

func (model *ExitModel) Remove(position contracts.Position) {
	delete(model.series, exitPositionKey(position))
}

func (model *ExitModel) Observe(observation ExitObservation, settings config.AutomationConfig) ExitDecision {
	if model == nil {
		return ExitDecision{Reason: "exit model unavailable"}
	}
	now := observation.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	entry, entryErr := observation.Position.EntryPrice.Float64()
	mark, markErr := observation.Mark.Float64()
	stop, stopErr := observation.StopLoss.Float64()
	if entryErr != nil || markErr != nil || stopErr != nil || entry <= 0 || mark <= 0 || stop <= 0 {
		return ExitDecision{Reason: "价格输入无效"}
	}
	riskDistance := math.Abs(entry - stop)
	if riskDistance <= 0 {
		return ExitDecision{Reason: "初始止损距离无效"}
	}
	direction := 1.0
	if observation.Position.Side == contracts.SideShort {
		direction = -1
	}
	currentR := direction * (mark - entry) / riskDistance
	key := exitPositionKey(observation.Position)
	state := model.series[key]
	if state == nil {
		state = &exitSeries{previousMark: mark, peakR: math.Max(0, currentR), firstSeen: now}
		model.series[key] = state
	}
	if state.previousMark > 0 {
		priceReturn := (mark - state.previousMark) / state.previousMark
		state.variance = settings.EWMALambda*state.variance + (1-settings.EWMALambda)*priceReturn*priceReturn
	}
	state.previousMark = mark
	state.samples++
	state.peakR = math.Max(state.peakR, currentR)
	volatilityR := math.Sqrt(math.Max(0, state.variance)) * entry / riskDistance
	trailDistance := math.Max(settings.TrailGivebackR, settings.VolatilityMultiplier*volatilityR)
	trailFloor := state.peakR - trailDistance
	openedAt := observation.OpenedAt.UTC()
	if openedAt.IsZero() {
		openedAt = state.firstSeen
	}
	holding := now.Sub(openedAt)
	if holding < 0 {
		holding = 0
	}

	reason := ""
	if currentR >= settings.ProfitTargetR {
		reason = fmt.Sprintf("理想收益退出：当前 %.2fR ≥ 目标 %.2fR", currentR, settings.ProfitTargetR)
	} else if currentR <= -settings.LossCutR {
		reason = fmt.Sprintf("行情恶化退出：当前 %.2fR ≤ -%.2fR", currentR, settings.LossCutR)
	} else if state.samples >= settings.ExitMinSamples && state.peakR >= settings.TrailActivationR && currentR <= trailFloor {
		reason = fmt.Sprintf("波动率跟踪退出：当前 %.2fR ≤ 动态底线 %.2fR", currentR, trailFloor)
	} else if holding >= time.Duration(settings.MaxHoldingMinutes)*time.Minute && currentR <= settings.TimeStopMinR {
		reason = fmt.Sprintf("时间止损：持仓 %s 且当前 %.2fR ≤ %.2fR", holding.Round(time.Minute), currentR, settings.TimeStopMinR)
	}
	if reason == "" {
		state.confirmations = 0
	} else {
		state.confirmations++
	}
	return ExitDecision{
		Trigger: state.confirmations >= settings.ExitConfirmations,
		Reason:  reason, CurrentR: currentR, PeakR: state.peakR,
		VolatilityR: volatilityR, TrailFloorR: trailFloor, Samples: state.samples,
		Confirmations: state.confirmations, Holding: holding,
	}
}

func exitPositionKey(position contracts.Position) string {
	return position.Venue + "\x00" + position.Symbol + "\x00" + string(position.Instrument) + "\x00" + string(position.Side)
}
