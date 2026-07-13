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
		Symbol: proposal.Symbol, ProposalRef: runID, Kind: "entry", Score: score,
		PNLQuote: contracts.Zero(), SlippageBPS: result.SlippageBPS,
		Lessons: lessons, Notes: fmt.Sprintf("%s %s 入场执行=%s；盈亏将在平仓后归因", proposal.Side, proposal.Symbol, result.Status),
		TS: reviewer.Now().UTC(),
	}, nil
}

func (reviewer Reviewer) RunClosed(
	ctx context.Context,
	position contracts.Position,
	result contracts.ExecutionResult,
	pnl contracts.Decimal,
	reference string,
) (contracts.TradeReview, error) {
	if err := contextError(ctx); err != nil {
		return contracts.TradeReview{}, err
	}
	lessons := make(contracts.List[string], 0, 3)
	score := 0.5
	switch {
	case pnl.IsPositive():
		score = 0.8
		lessons = append(lessons, fmt.Sprintf("平仓实现盈利 %s，保留入场依据并检查是否过早止盈。", pnl.String()))
	case pnl.IsNegative():
		score = 0.3
		lessons = append(lessons, fmt.Sprintf("平仓实现亏损 %s，复核入场信号、止损距离与市场状态。", pnl.String()))
	default:
		lessons = append(lessons, "平仓盈亏接近零，交易成本可能吞噬信号优势。")
	}
	if result.SlippageBPS != nil && result.SlippageBPS.Cmp(contracts.MustDecimal("20")) > 0 {
		score -= 0.2
		lessons = append(lessons, fmt.Sprintf("平仓滑点 %sbps 偏高，后续考虑限价或拆单。", result.SlippageBPS.String()))
	}
	if reviewer.Now == nil {
		reviewer.Now = time.Now
	}
	score = math.Max(0, math.Min(1, score))
	return contracts.TradeReview{
		Symbol: position.Symbol, ProposalRef: reference, Kind: "close", Score: score,
		PNLQuote: pnl, SlippageBPS: result.SlippageBPS, Lessons: lessons,
		Notes: fmt.Sprintf("%s %s 平仓执行=%s", position.Side, position.Symbol, result.Status),
		TS:    reviewer.Now().UTC(),
	}, nil
}
