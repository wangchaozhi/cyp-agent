package agents

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type Analyst interface {
	ID() contracts.AgentID
	Run(context.Context, contracts.MarketSnapshot, AgentContext) (contracts.AnalystReport, error)
}

type TechnicalAnalyst struct{}
type DerivativesAnalyst struct{}
type SentimentAnalyst struct{}
type OnchainAnalyst struct{}

func (TechnicalAnalyst) ID() contracts.AgentID   { return contracts.AgentTechnical }
func (DerivativesAnalyst) ID() contracts.AgentID { return contracts.AgentDerivatives }
func (SentimentAnalyst) ID() contracts.AgentID   { return contracts.AgentSentiment }
func (OnchainAnalyst) ID() contracts.AgentID     { return contracts.AgentOnchain }

func AllAnalysts() []Analyst {
	return []Analyst{TechnicalAnalyst{}, DerivativesAnalyst{}, SentimentAnalyst{}, OnchainAnalyst{}}
}

// RunAnalysts executes the fixed analyst panel concurrently while preserving
// deterministic report order. A single analyst failure degrades only that
// dimension; parent context cancellation stops the whole panel.
func RunAnalysts(
	ctx context.Context,
	panel []Analyst,
	snapshot contracts.MarketSnapshot,
	agentContext AgentContext,
) (contracts.List[contracts.AnalystReport], error) {
	if panel == nil {
		panel = AllAnalysts()
	}
	reports := make(contracts.List[contracts.AnalystReport], len(panel))
	var wait sync.WaitGroup
	wait.Add(len(panel))
	for index, analyst := range panel {
		index, analyst := index, analyst
		go func() {
			defer wait.Done()
			report, err := analyst.Run(ctx, snapshot, agentContext)
			if err != nil {
				reports[index] = degradedReport(analyst.ID(), "分析失败，已隔离该维度")
				return
			}
			reports[index] = report
		}()
	}
	wait.Wait()
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return reports, nil
}

func (TechnicalAnalyst) Run(
	ctx context.Context,
	snapshot contracts.MarketSnapshot,
	_ AgentContext,
) (contracts.AnalystReport, error) {
	if err := contextError(ctx); err != nil {
		return contracts.AnalystReport{}, err
	}
	values := indicatorSnapshot(snapshot.OHLCV)
	if values.lastClose == nil {
		return degradedReport(contracts.AgentTechnical, "无 K 线数据"), nil
	}
	votes := make([]Vote, 0, 4)
	if values.smaFast != nil && values.smaSlow != nil {
		sign := -1.0
		operator := "<"
		if *values.smaFast > *values.smaSlow {
			sign, operator = 1, ">"
		}
		votes = append(votes, Vote{Sign: sign, Weight: 1,
			Signal: contracts.Signal{Name: "trend", Value: "sma20" + operator + "sma50"}})
	}
	if values.macd != nil && values.macdSignal != nil {
		sign := -1.0
		if *values.macd > *values.macdSignal {
			sign = 1
		}
		votes = append(votes, Vote{Sign: sign, Weight: 0.8,
			Signal: contracts.Signal{Name: "macd", Value: fmt.Sprintf("%.2f vs %.2f", *values.macd, *values.macdSignal)}})
	}
	if values.rsi != nil {
		vote := Vote{Weight: 0.3, Signal: contracts.Signal{Name: "rsi", Value: fmt.Sprintf("%.1f 中性", *values.rsi)}}
		if *values.rsi > 70 {
			vote.Sign, vote.Weight, vote.Signal.Value = -1, 0.6, fmt.Sprintf("%.1f 超买", *values.rsi)
		} else if *values.rsi < 30 {
			vote.Sign, vote.Weight, vote.Signal.Value = 1, 0.6, fmt.Sprintf("%.1f 超卖", *values.rsi)
		}
		votes = append(votes, vote)
	}
	if values.bbUpper != nil && values.bbLower != nil {
		if *values.lastClose > *values.bbUpper {
			votes = append(votes, Vote{Sign: -1, Weight: 0.4,
				Signal: contracts.Signal{Name: "bollinger", Value: "上轨外，超伸"}})
		} else if *values.lastClose < *values.bbLower {
			votes = append(votes, Vote{Sign: 1, Weight: 0.4,
				Signal: contracts.Signal{Name: "bollinger", Value: "下轨外，超跌"}})
		}
	}
	stance, confidence := Blend(votes)
	rationale := "技术面综合"
	if values.rsi != nil {
		cross := "死叉"
		macd, signal := 0.0, 0.0
		if values.macd != nil {
			macd = *values.macd
		}
		if values.macdSignal != nil {
			signal = *values.macdSignal
		}
		if macd > signal {
			cross = "金叉"
		}
		rationale = fmt.Sprintf("技术面：RSI=%.1f MACD=%s", *values.rsi, cross)
	}
	return reportFromVotes(contracts.AgentTechnical, stance, confidence, votes, rationale), nil
}

func (DerivativesAnalyst) Run(
	ctx context.Context,
	snapshot contracts.MarketSnapshot,
	_ AgentContext,
) (contracts.AnalystReport, error) {
	if err := contextError(ctx); err != nil {
		return contracts.AnalystReport{}, err
	}
	data := snapshot.Derivatives
	if data == nil || data.FundingRate == nil {
		return degradedReport(contracts.AgentDerivatives, "无衍生品数据"), nil
	}
	funding := *data.FundingRate
	votes := make([]Vote, 0, 2)
	switch {
	case funding.Cmp(contracts.MustDecimal("0.0003")) > 0:
		votes = append(votes, Vote{Sign: -1, Weight: 0.8,
			Signal: contracts.Signal{Name: "funding", Value: funding.String() + " 偏高，多头拥挤"}})
	case funding.Cmp(contracts.MustDecimal("-0.0003")) < 0:
		votes = append(votes, Vote{Sign: 1, Weight: 0.8,
			Signal: contracts.Signal{Name: "funding", Value: funding.String() + " 偏低，空头拥挤"}})
	default:
		votes = append(votes, Vote{Weight: 0.4,
			Signal: contracts.Signal{Name: "funding", Value: funding.String() + " 正常"}})
	}
	if data.LongShortRatio != nil {
		ratio := *data.LongShortRatio
		if ratio.Cmp(contracts.MustDecimal("1.15")) > 0 {
			votes = append(votes, Vote{Sign: -0.5, Weight: 0.5,
				Signal: contracts.Signal{Name: "ls_ratio", Value: ratio.String() + " 多头偏拥挤"}})
		} else if ratio.Cmp(contracts.MustDecimal("0.87")) < 0 {
			votes = append(votes, Vote{Sign: 0.5, Weight: 0.5,
				Signal: contracts.Signal{Name: "ls_ratio", Value: ratio.String() + " 空头偏拥挤"}})
		}
	}
	stance, confidence := Blend(votes)
	return reportFromVotes(contracts.AgentDerivatives, stance, confidence, votes,
		"衍生品：资金费 "+funding.String()), nil
}

func (SentimentAnalyst) Run(
	ctx context.Context,
	snapshot contracts.MarketSnapshot,
	_ AgentContext,
) (contracts.AnalystReport, error) {
	if err := contextError(ctx); err != nil {
		return contracts.AnalystReport{}, err
	}
	data := snapshot.Sentiment
	if data == nil || data.FearGreed == nil {
		return degradedReport(contracts.AgentSentiment, "无情绪数据"), nil
	}
	fearGreed := *data.FearGreed
	votes := make([]Vote, 0, 2)
	switch {
	case fearGreed < 25:
		votes = append(votes, Vote{Sign: 1, Weight: 0.6,
			Signal: contracts.Signal{Name: "fear_greed", Value: fmt.Sprintf("%d 极端恐惧", fearGreed)}})
	case fearGreed > 75:
		votes = append(votes, Vote{Sign: -1, Weight: 0.6,
			Signal: contracts.Signal{Name: "fear_greed", Value: fmt.Sprintf("%d 极端贪婪", fearGreed)}})
	default:
		votes = append(votes, Vote{Weight: 0.3,
			Signal: contracts.Signal{Name: "fear_greed", Value: fmt.Sprintf("%d 中性", fearGreed)}})
	}
	if data.NewsScore != nil {
		value, err := data.NewsScore.Float64()
		if err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) {
			votes = append(votes, Vote{Sign: math.Max(-1, math.Min(1, value)), Weight: 0.4,
				Signal: contracts.Signal{Name: "news", Value: data.NewsScore.String()}})
		}
	}
	stance, confidence := Blend(votes)
	return reportFromVotes(contracts.AgentSentiment, stance, confidence, votes,
		fmt.Sprintf("情绪：恐贪 %d", fearGreed)), nil
}

func (OnchainAnalyst) Run(
	ctx context.Context,
	snapshot contracts.MarketSnapshot,
	_ AgentContext,
) (contracts.AnalystReport, error) {
	if err := contextError(ctx); err != nil {
		return contracts.AnalystReport{}, err
	}
	data := snapshot.Onchain
	if data == nil || data.SmartMoneyFlow == nil {
		return degradedReport(contracts.AgentOnchain, "无链上数据（M0 未接入）"), nil
	}
	votes := make([]Vote, 0, 2)
	flow := *data.SmartMoneyFlow
	if flow.IsPositive() {
		votes = append(votes, Vote{Sign: 1, Weight: 0.8,
			Signal: contracts.Signal{Name: "smart_money", Value: "净流入 " + flow.String()}})
	} else if flow.IsNegative() {
		votes = append(votes, Vote{Sign: -1, Weight: 0.8,
			Signal: contracts.Signal{Name: "smart_money", Value: "净流出 " + flow.String()}})
	}
	if data.ExchangeNetflow != nil {
		sign := 1.0
		if data.ExchangeNetflow.IsPositive() {
			sign = -1
		}
		votes = append(votes, Vote{Sign: sign, Weight: 0.5,
			Signal: contracts.Signal{Name: "exch_netflow", Value: data.ExchangeNetflow.String()}})
	}
	stance, confidence := Blend(votes)
	return reportFromVotes(contracts.AgentOnchain, stance, confidence, votes, "链上：聪明钱流向"), nil
}

func degradedReport(agent contracts.AgentID, rationale string) contracts.AnalystReport {
	return contracts.AnalystReport{
		Agent: agent, Stance: contracts.StanceNeutral, Confidence: 0.2,
		Signals: contracts.List[contracts.Signal]{}, Rationale: rationale, Degraded: true,
	}
}

func reportFromVotes(
	agent contracts.AgentID,
	stance contracts.Stance,
	confidence float64,
	votes []Vote,
	rationale string,
) contracts.AnalystReport {
	signals := make(contracts.List[contracts.Signal], 0, len(votes))
	for _, vote := range votes {
		signals = append(signals, vote.Signal)
	}
	return contracts.AnalystReport{
		Agent: agent, Stance: stance, Confidence: confidence,
		Signals: signals, Rationale: rationale,
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
