package orchestrator

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/riskstate"
)

const addOnModelVersion = "risk-pyramid-v1"

type AddOnEvaluation struct {
	Allowed bool                 `json:"allowed"`
	Reason  string               `json:"reason"`
	Plan    *contracts.AddOnPlan `json:"plan,omitempty"`
}

// evaluateAddOn permits pyramiding only into a profitable, strongly confirmed
// trend. Every additional tranche receives an exponentially smaller risk
// budget and is capped as a fraction of the existing position.
func evaluateAddOn(
	settings config.Settings,
	proposal contracts.TradeProposal,
	position contracts.Position,
	mark contracts.Decimal,
	equity contracts.Decimal,
	trades []riskstate.TradeRecord,
	now time.Time,
) (contracts.TradeProposal, AddOnEvaluation) {
	deny := func(reason string) (contracts.TradeProposal, AddOnEvaluation) {
		return proposal, AddOnEvaluation{Reason: reason}
	}
	automation := settings.Automation
	if !automation.Enabled || !automation.ScanEnabled || !automation.EntryEnabled ||
		!automation.ApprovalEnabled || !automation.AddEnabled {
		return deny("自动加仓已关闭")
	}
	if proposal.Side != position.Side || proposal.Instrument != position.Instrument {
		return deny("加仓方向或品种与现有仓位不一致")
	}
	if proposal.Confidence < automation.AddMinConfidence {
		return deny("信号置信度低于加仓门槛")
	}
	if proposal.StopLoss == nil || !mark.IsPositive() || !equity.IsPositive() {
		return deny("加仓缺少有效价格、止损或账户权益")
	}
	stopDistance := mark.Sub(*proposal.StopLoss).Abs()
	if !stopDistance.IsPositive() {
		return deny("加仓止损距离无效")
	}
	profitDistance := mark.Sub(position.EntryPrice)
	if position.Side == contracts.SideShort {
		profitDistance = position.EntryPrice.Sub(mark)
	}
	profitRDecimal, err := profitDistance.Quo(stopDistance)
	if err != nil {
		return deny("无法计算现有仓位盈利 R")
	}
	profitR, err := profitRDecimal.Float64()
	if err != nil || profitR < automation.AddMinProfitR {
		return deny(fmt.Sprintf("现有仓位盈利 %.2fR，低于加仓门槛 %.2fR", profitR, automation.AddMinProfitR))
	}

	entries, lastEntry := activeEntryCount(position, trades)
	addIndex := entries
	if addIndex < 1 {
		addIndex = 1
	}
	if addIndex > automation.MaxAddsPerPosition {
		return deny(fmt.Sprintf("已达到最多 %d 次加仓", automation.MaxAddsPerPosition))
	}
	if !lastEntry.IsZero() && automation.AddCooldownMinutes > 0 {
		cooldownUntil := lastEntry.Add(time.Duration(automation.AddCooldownMinutes) * time.Minute)
		if now.Before(cooldownUntil) {
			return deny(fmt.Sprintf("加仓冷却中，%s 后可再次评估", cooldownUntil.UTC().Format(time.RFC3339)))
		}
	}

	baseRisk, err := settings.Risk.MaxRiskPerTrade.Float64()
	if err != nil || baseRisk <= 0 {
		return deny("单笔风险预算无效")
	}
	riskFraction := baseRisk * math.Pow(automation.AddRiskDecay, float64(addIndex))
	riskDecimal, err := contracts.ParseDecimal(strconv.FormatFloat(riskFraction, 'g', 17, 64))
	if err != nil {
		return deny("加仓递减风险预算无法量化")
	}
	stopFraction, err := stopDistance.Quo(mark)
	if err != nil || !stopFraction.IsPositive() {
		return deny("加仓止损比例无效")
	}
	riskSized, err := equity.Mul(riskDecimal).QuoScale(stopFraction, 2, contracts.RoundDown)
	if err != nil || !riskSized.IsPositive() {
		return deny("加仓风险预算不足")
	}
	existingNotional := position.NotionalAt(mark)
	maxFraction, err := contracts.ParseDecimal(strconv.FormatFloat(automation.AddMaxPositionFraction, 'g', 17, 64))
	if err != nil {
		return deny("加仓比例上限无法量化")
	}
	trancheCap := existingNotional.Mul(maxFraction)
	recommended := decimalMinimum(proposal.SizeQuote, riskSized, trancheCap)
	recommended, _ = recommended.QuoScale(contracts.NewDecimalFromInt64(1), 2, contracts.RoundDown)
	if !recommended.IsPositive() || recommended.Cmp(automation.MinEntryQuote) < 0 {
		return deny("递减风险预算后的加仓金额低于最小下单金额")
	}

	plan := &contracts.AddOnPlan{
		Model: addOnModelVersion, ExistingNotionalQuote: existingNotional,
		ExistingLeverage: position.Leverage, ProfitR: profitR,
		AddIndex: addIndex, MaxAdds: automation.MaxAddsPerPosition,
		RiskDecay: automation.AddRiskDecay, RiskFraction: riskFraction,
		MaxPositionFraction:      automation.AddMaxPositionFraction,
		RecommendedNotionalQuote: recommended, CooldownMinutes: automation.AddCooldownMinutes,
	}
	proposal.SizeQuote = recommended
	proposal.AddOnPlan = plan
	proposal.Thesis += fmt.Sprintf("（自动加仓 %d/%d：现仓盈利 %.2fR，递减风险预算 %.3f%%）",
		addIndex, automation.MaxAddsPerPosition, profitR, riskFraction*100)
	return proposal, AddOnEvaluation{Allowed: true, Reason: "盈利趋势、冷却期和递减风险预算均通过", Plan: plan}
}

func activeEntryCount(position contracts.Position, trades []riskstate.TradeRecord) (int, time.Time) {
	count := 0
	trackedBase := contracts.Zero()
	lastEntry := time.Time{}
	for index := len(trades) - 1; index >= 0; index-- {
		trade := trades[index]
		if trade.Symbol != position.Symbol || trade.Instrument != position.Instrument {
			continue
		}
		if trade.Kind == "close" {
			break
		}
		if trade.Kind == "open" && trade.Side == position.Side {
			count++
			trackedBase = trackedBase.Add(trade.SizeBase)
			if trade.TS.After(lastEntry) {
				lastEntry = trade.TS
			}
		}
	}
	// Positions imported from the venue can predate the local ledger. Treat the
	// untracked base as the initial entry so subsequent add indices stay safe.
	if count == 0 || trackedBase.Cmp(position.SizeBase.Mul(contracts.MustDecimal("0.99"))) < 0 {
		count++
	}
	return count, lastEntry
}

func decimalMinimum(values ...contracts.Decimal) contracts.Decimal {
	result := values[0]
	for _, value := range values[1:] {
		if value.Cmp(result) < 0 {
			result = value
		}
	}
	return result
}
