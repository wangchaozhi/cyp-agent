package agents

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type Reviewer struct {
	Now func() time.Time
}

func NewReviewer() Reviewer { return Reviewer{Now: time.Now} }

func (reviewer Reviewer) Run(
	ctx context.Context,
	proposal contracts.TradeProposal,
	result contracts.ExecutionResult,
	_ AgentContext,
	runID string,
) (contracts.TradeReview, error) {
	if err := contextError(ctx); err != nil {
		return contracts.TradeReview{}, err
	}
	lessons := make(contracts.List[string], 0, 2)
	score := 0.6
	if result.Status != contracts.OrderStatusFilled {
		score = 0.2
		reason := "未知"
		if result.Error != nil && *result.Error != "" {
			reason = truncateRunes(redactSensitive(*result.Error), 1000)
		}
		lessons = append(lessons,
			fmt.Sprintf("执行失败（%s）：%s，检查 preflight 与场所可用性。", result.Status, reason))
	} else {
		if result.SlippageBPS != nil && result.SlippageBPS.Cmp(contracts.MustDecimal("20")) > 0 {
			score -= 0.2
			lessons = append(lessons,
				fmt.Sprintf("滑点偏高 %sbps，考虑限价入场或拆单。", result.SlippageBPS.String()))
		}
		if proposal.Confidence < 0.3 {
			lessons = append(lessons, "入场置信度偏低，信号偏弱时可缩仓或观望。")
		}
	}
	if reviewer.Now == nil {
		reviewer.Now = time.Now
	}
	score = math.Max(0, math.Min(1, score))
	return contracts.TradeReview{
		Symbol: proposal.Symbol, ProposalRef: runID, Score: score,
		PNLQuote: contracts.Zero(), SlippageBPS: result.SlippageBPS,
		Lessons: lessons, Notes: fmt.Sprintf("%s %s 执行=%s", proposal.Side, proposal.Symbol, result.Status),
		TS: reviewer.Now().UTC(),
	}, nil
}
