package agents

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/llm"
	"github.com/wangchaozhi/cyp-agent/internal/risk"
)

var defaultWeights = map[contracts.AgentID]float64{
	contracts.AgentTechnical: 1.0, contracts.AgentDerivatives: 0.9,
	contracts.AgentSentiment: 0.6, contracts.AgentOnchain: 0.8,
}

func DefaultWeights() map[contracts.AgentID]float64 { return cloneWeights(defaultWeights) }

type StrategyConfig struct {
	Weights        map[contracts.AgentID]float64
	EnterThreshold float64
	KStop          contracts.Decimal
	KTakeProfit    contracts.Decimal
	RiskPerTrade   *contracts.Decimal
	StopMode       string
	VolLambda      float64
	VolTarget      *float64
}

func DefaultStrategyConfig() StrategyConfig {
	weights := make(map[contracts.AgentID]float64, len(defaultWeights))
	for agent, weight := range defaultWeights {
		weights[agent] = weight
	}
	return StrategyConfig{
		Weights: weights, EnterThreshold: 0.12,
		KStop: contracts.MustDecimal("2"), KTakeProfit: contracts.MustDecimal("3"),
		StopMode: "atr", VolLambda: 0.94,
	}
}

func (strategy StrategyConfig) Validate() error {
	if math.IsNaN(strategy.EnterThreshold) || math.IsInf(strategy.EnterThreshold, 0) || strategy.EnterThreshold <= 0 || strategy.EnterThreshold > 1 {
		return errors.New("strategy enter threshold must be greater than 0 and at most 1")
	}
	if strategy.KStop.IsNegative() || strategy.KTakeProfit.IsNegative() {
		return errors.New("strategy stop and take-profit multipliers cannot be negative")
	}
	if strategy.RiskPerTrade != nil && strategy.RiskPerTrade.IsNegative() {
		return errors.New("strategy risk per trade cannot be negative")
	}
	if strategy.StopMode != "atr" && strategy.StopMode != "vol" {
		return errors.New("strategy stop mode must be atr or vol")
	}
	if math.IsNaN(strategy.VolLambda) || strategy.VolLambda < 0 || strategy.VolLambda > 1 {
		return errors.New("strategy EWMA lambda must be between 0 and 1")
	}
	if strategy.VolTarget != nil && (math.IsNaN(*strategy.VolTarget) || math.IsInf(*strategy.VolTarget, 0) || *strategy.VolTarget <= 0) {
		return errors.New("strategy volatility target must be positive and finite")
	}
	for _, weight := range strategy.Weights {
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
			return errors.New("strategy weights must be finite and non-negative")
		}
	}
	return nil
}

type Strategist struct{ config StrategyConfig }

func NewStrategist(strategy *StrategyConfig) *Strategist {
	value := DefaultStrategyConfig()
	if strategy != nil {
		value = *strategy
		value.Weights = cloneWeights(strategy.Weights)
		if value.Weights == nil {
			value.Weights = cloneWeights(defaultWeights)
		}
		if value.StopMode == "" {
			value.StopMode = "atr"
		}
	}
	if err := value.Validate(); err != nil {
		value = DefaultStrategyConfig()
	}
	return &Strategist{config: value}
}

func cloneWeights(source map[contracts.AgentID]float64) map[contracts.AgentID]float64 {
	if source == nil {
		return nil
	}
	copy := make(map[contracts.AgentID]float64, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func (strategist *Strategist) Run(
	ctx context.Context,
	reports []contracts.AnalystReport,
	snapshot contracts.MarketSnapshot,
	equity contracts.Decimal,
	riskConfig config.RiskConfig,
	agentContext AgentContext,
	venueID string,
	positions []contracts.Position,
) (contracts.TradeProposal, error) {
	if err := contextError(ctx); err != nil {
		return contracts.TradeProposal{}, err
	}
	if strategist == nil {
		strategist = NewStrategist(nil)
	}
	if venueID == "" {
		venueID = "paper"
	}
	supporting := supportingAgents(reports)
	net, confidence := strategist.composite(reports)
	values := indicatorSnapshot(snapshot.OHLCV)
	if values.lastClose == nil || math.Abs(net) < strategist.config.EnterThreshold || len(snapshot.OHLCV) == 0 {
		return flatProposal(snapshot.Symbol, venueID, confidence,
			"多维信号不足或冲突，本轮不开仓。", supporting), nil
	}
	ref := snapshot.OHLCV[len(snapshot.OHLCV)-1].Close
	if !ref.IsPositive() {
		return flatProposal(snapshot.Symbol, venueID, confidence, "参考价无效，本轮不开仓。", supporting), nil
	}

	ewmaSigma := ewmaVolatility(snapshot.OHLCV, strategist.config.VolLambda)
	volatilityUnit := ref.Mul(contracts.MustDecimal("0.02"))
	if strategist.config.StopMode == "vol" && ewmaSigma > 0 {
		if value, err := decimalFromFloat(ewmaSigma); err == nil {
			volatilityUnit = ref.Mul(value)
		}
	} else if values.atr != nil && *values.atr > 0 {
		if value, err := decimalFromFloat(*values.atr); err == nil {
			volatilityUnit = value
		}
	}

	side := contracts.SideLong
	if net < 0 {
		side = contracts.SideShort
	}
	instrument := contracts.InstrumentSpot
	leverage := 1.0
	isPerpetualSymbol := strings.Contains(snapshot.Symbol, ":")
	if isPerpetualSymbol && !agentContext.AllowPerp {
		return flatProposal(snapshot.Symbol, venueID, confidence,
			fmt.Sprintf("%s 为永续合约标的，但未开启 allow_perp，本轮不交易。", snapshot.Symbol), supporting), nil
	}
	if isPerpetualSymbol {
		instrument = contracts.InstrumentPerp
	} else if agentContext.AllowPerp {
		instrument = contracts.InstrumentPerp
	}
	if side == contracts.SideShort && instrument == contracts.InstrumentSpot {
		return flatProposal(snapshot.Symbol, venueID, confidence,
			"看空信号但现货无法裸卖空（无持仓可减），本轮观望。", supporting), nil
	}

	stopDistance := strategist.config.KStop.Mul(volatilityUnit)
	takeProfitDistance := strategist.config.KTakeProfit.Mul(volatilityUnit)
	stop := ref.Sub(stopDistance)
	takeProfit := ref.Add(takeProfitDistance)
	if side == contracts.SideShort {
		stop = ref.Add(stopDistance)
		takeProfit = ref.Sub(takeProfitDistance)
	}
	stopFraction, err := ref.Sub(stop).Abs().Quo(ref)
	if err != nil || !stopFraction.IsPositive() {
		return flatProposal(snapshot.Symbol, venueID, confidence, "止损距离无效，本轮不开仓。", supporting), nil
	}

	var size contracts.Decimal
	if strategist.config.VolTarget != nil && ewmaSigma > 0 {
		target, targetErr := decimalFromFloat(*strategist.config.VolTarget)
		sigma, sigmaErr := decimalFromFloat(ewmaSigma)
		if targetErr == nil && sigmaErr == nil {
			size, err = equity.Mul(target).Quo(sigma)
		}
	} else {
		riskPerTrade := riskConfig.MaxRiskPerTrade
		if strategist.config.RiskPerTrade != nil {
			riskPerTrade = *strategist.config.RiskPerTrade
		}
		size, err = equity.Mul(riskPerTrade).Quo(stopFraction)
	}
	if err != nil {
		return contracts.TradeProposal{}, err
	}
	size = size.Mul(contracts.MustDecimal("0.995"))
	positionCap := equity.Mul(riskConfig.MaxPositionPct)
	if size.Cmp(positionCap) > 0 {
		size = positionCap
	}

	cluster := correlationCluster(snapshot.Symbol)
	clusterExposure := directionalClusterExposure(positions, cluster, side, snapshot.Symbol, ref)
	clusterLimit := equity.Mul(riskConfig.MaxCorrelatedExposure)
	portfolioNote := ""
	if clusterLimit.IsPositive() && clusterExposure.Cmp(clusterLimit.Mul(contracts.MustDecimal("0.8"))) >= 0 {
		headroom := clusterLimit.Sub(clusterExposure)
		if !headroom.IsPositive() {
			return flatProposal(snapshot.Symbol, venueID, confidence,
				fmt.Sprintf("%s 簇同向敞口已达上限（%s/%s），本轮规避。", cluster, clusterExposure, clusterLimit), supporting), nil
		}
		if size.Cmp(headroom) > 0 {
			size = headroom
		}
		portfolioNote = fmt.Sprintf("（%s 簇敞口接近上限，已按剩余额度缩仓）", cluster)
	}
	if !size.IsPositive() {
		return flatProposal(snapshot.Symbol, venueID, confidence, "风险预算无可用仓位，本轮不开仓。", supporting), nil
	}
	if !takeProfit.IsPositive() {
		return flatProposal(snapshot.Symbol, venueID, confidence, "止盈价格无效，本轮不开仓。", supporting), nil
	}

	thesis := strategist.ruleThesis(reports, net, side) + portfolioNote
	if agentContext.LLMEnabled() {
		held := heldPositions(positions)
		lessons := recentLessons(agentContext.Lessons, 5)
		llmContext := llm.WithUsageMetadata(ctx, llm.UsageMetadata{Agent: "strategist"})
		refined, llmErr := agentContext.LLM.Text(llmContext,
			"你是加密交易策略官，用两句话中文说明该交易的核心逻辑。方向与仓位已由规则确定，你只解释依据：不要建议观望、不要否定或反转方向，不要给出与输入不同的价格或仓位。",
			fmt.Sprintf("方向=%s 综合分=%.2f 当前组合持仓=%s 历史复盘经验=%s 依据=%s", side, net, held, lessons, thesis),
			true,
		)
		if llmErr == nil && strings.TrimSpace(refined) != "" {
			thesis = redactSensitive(strings.TrimSpace(refined))
			thesis = truncateRunes(thesis, 4000)
		}
	}

	quantizedSize, _ := size.QuoScale(contracts.NewDecimalFromInt64(1), 2, contracts.RoundDown)
	quantizedStop, _ := stop.QuoScale(contracts.NewDecimalFromInt64(1), 2, contracts.RoundHalfEven)
	var leveragePlan *contracts.LeveragePlan
	if instrument == contracts.InstrumentPerp {
		modelStopFraction, fractionErr := ref.Sub(quantizedStop).Abs().Quo(ref)
		if fractionErr != nil || !modelStopFraction.IsPositive() {
			return flatProposal(snapshot.Symbol, venueID, confidence,
				"量化后的止损距离无效，本轮不开仓。", supporting), nil
		}
		volatilityFraction := contracts.Zero()
		if ewmaSigma > 0 {
			volatilityFraction, err = decimalFromFloat(ewmaSigma)
			if err != nil {
				return contracts.TradeProposal{}, err
			}
		}
		calculation, leverageErr := risk.CalculateLeverage(risk.LeverageInput{
			EquityQuote: equity, ProposedNotionalQuote: quantizedSize,
			StopFraction: modelStopFraction, VolatilityFraction: volatilityFraction,
			MaxLeverage: riskConfig.MaxLeverage, MaxMarginPct: riskConfig.MaxMarginPct,
			LeverageStep: riskConfig.LeverageStep, MinLiquidationBuffer: riskConfig.MinLiqBuffer,
			StopLossBufferMultiple:   riskConfig.LiqStopMultiple,
			VolatilityBufferMultiple: riskConfig.LiqVolMultiple,
			LiquidationReservePct:    riskConfig.LiqReservePct,
		})
		if leverageErr != nil {
			return flatProposal(snapshot.Symbol, venueID, confidence,
				"杠杆压力模型无安全可行解，本轮不开仓。", supporting), nil
		}
		quantizedSize = calculation.NotionalQuote
		leverage = calculation.Plan.SelectedLeverage
		plan := calculation.Plan
		leveragePlan = &plan
	}
	entryReference := ref
	quantizedTakeProfit, _ := takeProfit.QuoScale(contracts.NewDecimalFromInt64(1), 2, contracts.RoundHalfEven)
	proposal := contracts.TradeProposal{
		Symbol: snapshot.Symbol, Venue: venueID, Side: side, Instrument: instrument,
		SizeQuote: quantizedSize, Leverage: leverage, MarginMode: contracts.MarginModeIsolated,
		Entry: contracts.PricePlan{Type: contracts.EntryTypeMarket, Price: &entryReference}, StopLoss: &quantizedStop,
		TakeProfit: contracts.List[contracts.Decimal]{quantizedTakeProfit},
		Confidence: confidence, Thesis: thesis, SupportingReports: supporting,
		LeveragePlan: leveragePlan,
	}
	return proposal, proposal.Validate()
}

func (strategist *Strategist) composite(reports []contracts.AnalystReport) (float64, float64) {
	totalWeight := 0.0
	net := 0.0
	for _, report := range reports {
		if report.Degraded {
			continue
		}
		weight, ok := strategist.config.Weights[report.Agent]
		if !ok {
			weight = 0.5
		}
		if weight <= 0 || math.IsNaN(report.Confidence) || math.IsInf(report.Confidence, 0) {
			continue
		}
		net += StanceSign(report.Stance) * math.Max(0, math.Min(1, report.Confidence)) * weight
		totalWeight += weight
	}
	if totalWeight > 0 {
		net /= totalWeight
	}
	return net, math.Min(1, math.Abs(net))
}

func (strategist *Strategist) ruleThesis(reports []contracts.AnalystReport, net float64, side contracts.Side) string {
	drivers := make([]string, 0, len(reports))
	for _, report := range reports {
		if !report.Degraded {
			drivers = append(drivers, fmt.Sprintf("%s:%s", report.Agent, report.Stance))
		}
	}
	return fmt.Sprintf("综合分 %+.2f → %s。驱动：%s。", net, side, strings.Join(drivers, ", "))
}

func flatProposal(symbol, venue string, confidence float64, thesis string, supporting contracts.List[string]) contracts.TradeProposal {
	return contracts.TradeProposal{
		Symbol: symbol, Venue: venue, Side: contracts.SideFlat, Instrument: contracts.InstrumentSpot,
		SizeQuote: contracts.Zero(), Leverage: 1, MarginMode: contracts.MarginModeIsolated,
		Entry: contracts.PricePlan{Type: contracts.EntryTypeMarket}, TakeProfit: contracts.List[contracts.Decimal]{},
		Confidence: confidence, Thesis: thesis, SupportingReports: supporting,
	}
}

func supportingAgents(reports []contracts.AnalystReport) contracts.List[string] {
	result := make(contracts.List[string], 0, len(reports))
	for _, report := range reports {
		if !report.Degraded {
			result = append(result, string(report.Agent))
		}
	}
	return result
}

func decimalFromFloat(value float64) (contracts.Decimal, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return contracts.Zero(), errors.New("cannot convert non-finite float to Decimal")
	}
	return contracts.ParseDecimal(strconv.FormatFloat(value, 'g', -1, 64))
}

func correlationCluster(symbol string) string {
	base := strings.ToUpper(strings.SplitN(symbol, "/", 2)[0])
	majors := map[string]struct{}{
		"BTC": {}, "ETH": {}, "BNB": {}, "SOL": {}, "XRP": {}, "ADA": {},
		"DOGE": {}, "TON": {}, "AVAX": {},
	}
	if _, ok := majors[base]; ok {
		return "major"
	}
	return "alt"
}

func directionalClusterExposure(
	positions []contracts.Position,
	cluster string,
	side contracts.Side,
	currentSymbol string,
	currentPrice contracts.Decimal,
) contracts.Decimal {
	net := contracts.Zero()
	for _, position := range positions {
		if correlationCluster(position.Symbol) != cluster {
			continue
		}
		price := position.EntryPrice
		if position.Symbol == currentSymbol {
			price = currentPrice
		}
		notional := position.NotionalAt(price)
		if position.Side == side {
			net = net.Add(notional)
		} else {
			net = net.Sub(notional)
		}
	}
	if net.IsNegative() {
		return contracts.Zero()
	}
	return net
}

func heldPositions(positions []contracts.Position) string {
	if len(positions) == 0 {
		return "无"
	}
	items := make([]string, 0, len(positions))
	for _, position := range positions {
		items = append(items, fmt.Sprintf("%s:%s", position.Symbol, position.Side))
	}
	return strings.Join(items, ", ")
}

func recentLessons(lessons []string, count int) string {
	if len(lessons) == 0 {
		return "无"
	}
	start := len(lessons) - count
	if start < 0 {
		start = 0
	}
	return redactSensitive(strings.Join(lessons[start:], "；"))
}

func truncateRunes(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}
