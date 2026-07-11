package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

var riskReviewSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "properties":{
    "risk_score":{"type":"number","minimum":0,"maximum":1},
    "escalate_reject":{"type":"boolean"},
    "notes":{"type":"string"}
  },
  "required":["risk_score"]
}`)

type riskReview struct {
	RiskScore      *float64 `json:"risk_score"`
	EscalateReject bool     `json:"escalate_reject"`
	Notes          string   `json:"notes"`
}

type RiskOfficer struct{}

func (RiskOfficer) Run(
	ctx context.Context,
	proposal contracts.TradeProposal,
	assessment contracts.RiskAssessment,
	reports []contracts.AnalystReport,
	agentContext AgentContext,
) (contracts.RiskAssessment, error) {
	if err := contextError(ctx); err != nil {
		return assessment, err
	}
	// The soft reviewer is structurally incapable of reviving a hard rejection.
	if assessment.Verdict == contracts.VerdictRejected || !agentContext.LLMEnabled() {
		return assessment, nil
	}
	drivers := make([]string, 0, len(reports))
	for _, report := range reports {
		drivers = append(drivers, fmt.Sprintf("%s:%s(%.2f)", report.Agent, report.Stance, report.Confidence))
	}
	var review riskReview
	err := agentContext.LLM.JSON(
		ctx,
		"你是加密交易风控官。只能收紧不能放宽：若发现 thesis 不自洽、极端行情/事件窗口、或与已有敞口叠加同向风险，可 escalate_reject=true。置信度偏低本身不是否决理由，应体现为抬高 risk_score。给出 0-1 风险分与简短中文说明。",
		fmt.Sprintf("提案：%s %s 仓位=%s 止损=%v 置信=%.2f\n分析：%s\n历史复盘经验：%s",
			proposal.Side, proposal.Symbol, proposal.SizeQuote, proposal.StopLoss, proposal.Confidence,
			strings.Join(drivers, "; "), recentLessons(agentContext.Lessons, 5)),
		riskReviewSchema,
		&review,
		false,
	)
	if err != nil {
		if contextError(ctx) != nil {
			return assessment, ctx.Err()
		}
		// Invalid JSON, provider failure, breaker, and budget exhaustion all
		// degrade to the deterministic hard-risk result.
		return assessment, nil
	}
	if review.RiskScore == nil || math.IsNaN(*review.RiskScore) || math.IsInf(*review.RiskScore, 0) || *review.RiskScore < 0 || *review.RiskScore > 1 {
		return assessment, nil
	}
	result := assessment
	result.HardViolations = append(contracts.List[string]{}, assessment.HardViolations...)
	if review.EscalateReject {
		result.Verdict = contracts.VerdictRejected
		note := truncateRunes(redactSensitive(strings.TrimSpace(review.Notes)), 1000)
		if note == "" {
			note = "软评审否决"
		}
		result.HardViolations = append(result.HardViolations, "risk_officer: "+note)
	}
	result.RiskScore = math.Max(assessment.RiskScore, *review.RiskScore)
	result.LLMNotes = redactSensitive(strings.TrimSpace(review.Notes))
	result.LLMNotes = truncateRunes(result.LLMNotes, 4000)
	result.LLMReviewed = true
	return result, nil
}
