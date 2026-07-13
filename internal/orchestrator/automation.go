package orchestrator

import (
	"fmt"
	"math"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

// AutoApprovalMetrics makes every mathematical gate auditable in checkpoints
// and the dashboard timeline. Confidence is used as a conservative probability
// proxy; it never bypasses the deterministic risk engine.
type AutoApprovalMetrics struct {
	Allowed       bool    `json:"allowed"`
	RewardRisk    float64 `json:"reward_risk"`
	ExpectedR     float64 `json:"expected_r"`
	KellyFraction float64 `json:"kelly_fraction"`
	Confidence    float64 `json:"confidence"`
	RiskScore     float64 `json:"risk_score"`
	Reason        string  `json:"reason"`
}

func evaluateAutoApproval(
	settings config.Settings,
	proposal contracts.TradeProposal,
	assessment contracts.RiskAssessment,
) AutoApprovalMetrics {
	result := AutoApprovalMetrics{Confidence: proposal.Confidence, RiskScore: assessment.RiskScore}
	if !automationApprovalEnabled(settings) {
		result.Reason = "自动审批已关闭"
		return result
	}
	allowedSymbols := settings.AutoSymbolsList()
	if len(allowedSymbols) == 0 {
		allowedSymbols = settings.WatchlistSymbols()
	}
	if !containsSymbol(allowedSymbols, proposal.Symbol) {
		result.Reason = "币种不在自动化白名单"
		return result
	}
	if assessment.RiskScore > settings.Automation.MaxRiskScore {
		result.Reason = "风险分超过自动审批上限"
		return result
	}
	approvalQuote := proposal.SizeQuote
	if assessment.AdjustedSizeQuote != nil && assessment.AdjustedSizeQuote.Cmp(approvalQuote) < 0 {
		approvalQuote = *assessment.AdjustedSizeQuote
	}
	if approvalQuote.Cmp(settings.Automation.MaxQuote) > 0 {
		result.Reason = "名义金额超过自动审批上限"
		return result
	}
	if proposal.Confidence < settings.Automation.MinConfidence {
		result.Reason = "置信度低于自动审批下限"
		return result
	}
	rewardRisk, ok := proposalRewardRisk(proposal)
	result.RewardRisk = rewardRisk
	if !ok || rewardRisk < settings.Automation.MinRewardRisk {
		result.Reason = "盈亏比低于自动审批下限"
		return result
	}
	p := math.Max(0, math.Min(1, proposal.Confidence))
	result.ExpectedR = p*rewardRisk - (1 - p)
	result.KellyFraction = result.ExpectedR / rewardRisk
	if result.ExpectedR <= 0 || result.KellyFraction <= 0 {
		result.Reason = "期望收益或 Kelly 比例不为正"
		return result
	}
	result.Allowed = true
	result.Reason = "数学审批策略通过"
	return result
}

func automationApprovalEnabled(settings config.Settings) bool {
	return settings.Automation.Enabled && settings.Automation.ApprovalEnabled
}

func containsSymbol(values []string, symbol string) bool {
	for _, value := range values {
		if value == symbol {
			return true
		}
	}
	return false
}

func proposalRewardRisk(proposal contracts.TradeProposal) (float64, bool) {
	if proposal.Entry.Price == nil || proposal.StopLoss == nil || len(proposal.TakeProfit) == 0 {
		return 0, false
	}
	entry, entryErr := proposal.Entry.Price.Float64()
	stop, stopErr := proposal.StopLoss.Float64()
	take, takeErr := proposal.TakeProfit[0].Float64()
	if entryErr != nil || stopErr != nil || takeErr != nil || entry <= 0 {
		return 0, false
	}
	var riskDistance, rewardDistance float64
	switch proposal.Side {
	case contracts.SideLong:
		riskDistance, rewardDistance = entry-stop, take-entry
	case contracts.SideShort:
		riskDistance, rewardDistance = stop-entry, entry-take
	default:
		return 0, false
	}
	if riskDistance <= 0 || rewardDistance <= 0 {
		return 0, false
	}
	return rewardDistance / riskDistance, true
}

func autoApprovalNote(metrics AutoApprovalMetrics) string {
	return fmt.Sprintf("%s：RR=%.2f EV=%.2fR Kelly=%.2f%% risk=%.2f confidence=%.2f",
		metrics.Reason, metrics.RewardRisk, metrics.ExpectedR, metrics.KellyFraction*100,
		metrics.RiskScore, metrics.Confidence)
}
