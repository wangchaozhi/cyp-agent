// Package risk implements deterministic hard risk controls. It has no LLM or
// network dependency and must run immediately before every order submission.
package risk

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

// Limits is an immutable risk policy snapshot assembled from runtime config.
type Limits struct {
	MaxRiskPerTrade        contracts.Decimal
	MaxPositionPct         contracts.Decimal
	MaxGrossExposure       contracts.Decimal
	MaxSymbolConcentration contracts.Decimal
	MaxCorrelatedExposure  contracts.Decimal
	MaxCVARPct             contracts.Decimal
	MaxOrdersPerHour       int
	MaxSlippageBPS         contracts.Decimal
	MaxLeverage            contracts.Decimal
	MinLiquidationBuffer   contracts.Decimal
	ForceIsolated          bool
	MinMarginRatio         contracts.Decimal
	MaxPriceImpact         contracts.Decimal
	MaxGasQuote            contracts.Decimal
	MinPoolTVL             contracts.Decimal
	ContractWhitelist      map[string]struct{}
	RequirePrivateMempool  bool
	DailyDrawdownLimit     contracts.Decimal
	WeeklyDrawdownLimit    contracts.Decimal
	MaxDrawdownLimit       contracts.Decimal
	MaxConsecutiveLosses   int
}

func DefaultLimits() Limits {
	return Limits{
		MaxRiskPerTrade:        contracts.MustDecimal("0.01"),
		MaxPositionPct:         contracts.MustDecimal("0.20"),
		MaxGrossExposure:       contracts.MustDecimal("1.00"),
		MaxSymbolConcentration: contracts.MustDecimal("0.30"),
		MaxCorrelatedExposure:  contracts.MustDecimal("0.50"),
		MaxCVARPct:             contracts.MustDecimal("0.03"),
		MaxOrdersPerHour:       10,
		MaxSlippageBPS:         contracts.MustDecimal("30"),
		MaxLeverage:            contracts.MustDecimal("3"),
		MinLiquidationBuffer:   contracts.MustDecimal("0.30"),
		ForceIsolated:          true,
		MinMarginRatio:         contracts.MustDecimal("0.05"),
		MaxPriceImpact:         contracts.MustDecimal("0.01"),
		MaxGasQuote:            contracts.MustDecimal("20"),
		MinPoolTVL:             contracts.MustDecimal("1000000"),
		ContractWhitelist:      map[string]struct{}{},
		RequirePrivateMempool:  true,
		DailyDrawdownLimit:     contracts.MustDecimal("0.03"),
		WeeklyDrawdownLimit:    contracts.MustDecimal("0.08"),
		MaxDrawdownLimit:       contracts.MustDecimal("0.15"),
		MaxConsecutiveLosses:   4,
	}
}

type action uint8

const (
	actionOK action = iota
	actionReject
	actionDownsize
)

type ruleResult struct {
	rule    string
	action  action
	reason  string
	maxSize *contracts.Decimal
}

func ok(rule string) ruleResult { return ruleResult{rule: rule} }

// Assess runs every rule in stable order so regression fixtures
// can compare both the verdict and the human-readable violation sequence.
func Assess(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) contracts.RiskAssessment {
	results := []ruleResult{
		ruleKillSwitch(proposal, ctx),
		ruleReconciling(proposal, ctx),
		ruleDrawdown(proposal, ctx, limits),
		ruleConsecutiveLosses(proposal, ctx, limits),
		ruleOrderRate(proposal, ctx, limits),
		ruleStopLoss(proposal, ctx),
		ruleLeverage(proposal, limits),
		ruleLiquidationBuffer(proposal, ctx, limits),
		ruleMarginMode(proposal, limits),
		ruleMaintenanceMargin(proposal, ctx, limits),
		ruleSlippage(ctx, limits),
		rulePriceImpact(ctx, limits),
		ruleInfiniteApproval(proposal, ctx),
		ruleContractWhitelist(proposal, ctx, limits),
		ruleMinPoolTVL(proposal, ctx, limits),
		ruleGasCap(ctx, limits),
		ruleMEVRoute(proposal, ctx, limits),
		rulePerTradeRisk(proposal, ctx, limits),
		rulePositionCap(proposal, ctx, limits),
		ruleGrossExposure(proposal, ctx, limits),
		ruleSymbolConcentration(proposal, ctx, limits),
		ruleCorrelatedExposure(proposal, ctx, limits),
		ruleCVAR(proposal, ctx, limits),
	}

	rejects := make(contracts.List[string], 0)
	for _, result := range results {
		if result.action == actionReject {
			rejects = append(rejects, result.rule+": "+result.reason)
		}
	}
	if len(rejects) > 0 {
		return contracts.RiskAssessment{
			Verdict: contracts.VerdictRejected, HardViolations: rejects,
			LLMNotes: "", RiskScore: 1, LLMReviewed: false,
		}
	}

	violations := make(contracts.List[string], 0)
	adjusted := proposal.SizeQuote
	for _, result := range results {
		if result.action != actionDownsize {
			continue
		}
		violations = append(violations, result.rule+": "+result.reason)
		if result.maxSize != nil && result.maxSize.Cmp(adjusted) < 0 {
			adjusted = *result.maxSize
		}
	}
	if len(violations) > 0 {
		return contracts.RiskAssessment{
			Verdict: contracts.VerdictDownsized, HardViolations: violations,
			AdjustedSizeQuote: &adjusted, LLMNotes: "",
			RiskScore: baseRiskScore(proposal, ctx, limits, adjusted), LLMReviewed: false,
		}
	}

	return contracts.RiskAssessment{
		Verdict: contracts.VerdictApproved, HardViolations: contracts.List[string]{},
		LLMNotes: "", RiskScore: baseRiskScore(proposal, ctx, limits, proposal.SizeQuote),
		LLMReviewed: false,
	}
}

func isOpen(proposal contracts.TradeProposal) bool {
	return proposal.Side == contracts.SideLong || proposal.Side == contracts.SideShort
}

func ruleKillSwitch(proposal contracts.TradeProposal, ctx contracts.RiskContext) ruleResult {
	if ctx.Kill && isOpen(proposal) {
		return ruleResult{"kill_switch", actionReject, "Kill Switch 已开启，拒绝新开仓", nil}
	}
	return ok("kill_switch")
}

func ruleReconciling(proposal contracts.TradeProposal, ctx contracts.RiskContext) ruleResult {
	if ctx.Reconciling && isOpen(proposal) {
		return ruleResult{"reconciling", actionReject, "对账未完成，冻结新开仓（仅允许减仓/平仓）", nil}
	}
	return ok("reconciling")
}

func ruleDrawdown(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if !isOpen(proposal) {
		return ok("drawdown_circuit")
	}
	if ctx.TotalDrawdown.Cmp(limits.MaxDrawdownLimit) >= 0 {
		return ruleResult{"drawdown_circuit", actionReject, fmt.Sprintf("总回撤 %s ≥ 熔断线 %s，全面停手", ctx.TotalDrawdown, limits.MaxDrawdownLimit), nil}
	}
	if ctx.WeeklyDrawdown.Cmp(limits.WeeklyDrawdownLimit) >= 0 {
		return ruleResult{"drawdown_circuit", actionReject, fmt.Sprintf("周回撤 %s ≥ %s，冻结开仓", ctx.WeeklyDrawdown, limits.WeeklyDrawdownLimit), nil}
	}
	if ctx.DailyDrawdown.Cmp(limits.DailyDrawdownLimit) >= 0 {
		return ruleResult{"drawdown_circuit", actionReject, fmt.Sprintf("日回撤 %s ≥ %s，冻结开仓", ctx.DailyDrawdown, limits.DailyDrawdownLimit), nil}
	}
	return ok("drawdown_circuit")
}

func ruleConsecutiveLosses(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if isOpen(proposal) && ctx.ConsecutiveLosses >= limits.MaxConsecutiveLosses {
		return ruleResult{"consecutive_losses", actionReject, fmt.Sprintf("连亏 %d 次 ≥ %d，进入冷静期", ctx.ConsecutiveLosses, limits.MaxConsecutiveLosses), nil}
	}
	return ok("consecutive_losses")
}

func ruleOrderRate(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if isOpen(proposal) && ctx.OrdersLastHour >= limits.MaxOrdersPerHour {
		return ruleResult{"order_rate", actionReject, fmt.Sprintf("近一小时下单 %d 次 ≥ 上限 %d", ctx.OrdersLastHour, limits.MaxOrdersPerHour), nil}
	}
	return ok("order_rate")
}

func ruleStopLoss(proposal contracts.TradeProposal, ctx contracts.RiskContext) ruleResult {
	if !isOpen(proposal) {
		return ok("stop_loss_required")
	}
	if proposal.StopLoss == nil {
		return ruleResult{"stop_loss_required", actionReject, "提案缺少止损，直接否决", nil}
	}
	if proposal.Side == contracts.SideLong && proposal.StopLoss.Cmp(ctx.RefPrice) >= 0 {
		return ruleResult{"stop_loss_required", actionReject, "多头止损价须低于当前价", nil}
	}
	if proposal.Side == contracts.SideShort && proposal.StopLoss.Cmp(ctx.RefPrice) <= 0 {
		return ruleResult{"stop_loss_required", actionReject, "空头止损价须高于当前价", nil}
	}
	return ok("stop_loss_required")
}

func ruleLeverage(proposal contracts.TradeProposal, limits Limits) ruleResult {
	if !isOpen(proposal) {
		return ok("leverage")
	}
	leverage, err := contracts.ParseDecimal(strconv.FormatFloat(proposal.Leverage, 'g', -1, 64))
	if err != nil || leverage.Cmp(limits.MaxLeverage) > 0 {
		return ruleResult{"leverage", actionReject, fmt.Sprintf("杠杆 %gx > 上限 %sx", proposal.Leverage, limits.MaxLeverage), nil}
	}
	return ok("leverage")
}

func ruleLiquidationBuffer(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if !isOpen(proposal) || proposal.Instrument != contracts.InstrumentPerp || ctx.EstimatedLiquidationPrice == nil || !ctx.RefPrice.IsPositive() {
		return ok("liq_buffer")
	}
	buffer := mustQuo(ctx.RefPrice.Sub(*ctx.EstimatedLiquidationPrice).Abs(), ctx.RefPrice)
	if buffer.Cmp(limits.MinLiquidationBuffer) < 0 {
		return ruleResult{"liq_buffer", actionReject, fmt.Sprintf("爆仓缓冲 %s < 下限 %s", buffer, limits.MinLiquidationBuffer), nil}
	}
	return ok("liq_buffer")
}

func ruleMarginMode(proposal contracts.TradeProposal, limits Limits) ruleResult {
	if isOpen(proposal) && proposal.Instrument == contracts.InstrumentPerp && limits.ForceIsolated && proposal.MarginMode != contracts.MarginModeIsolated {
		return ruleResult{"margin_mode", actionReject, fmt.Sprintf("合约须逐仓，当前 %s", proposal.MarginMode), nil}
	}
	return ok("margin_mode")
}

func ruleMaintenanceMargin(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if isOpen(proposal) && proposal.Instrument == contracts.InstrumentPerp && ctx.MarginRatio != nil && ctx.MarginRatio.Cmp(limits.MinMarginRatio) < 0 {
		return ruleResult{"maintenance_margin", actionReject, fmt.Sprintf("维持保证金率 %s < 下限 %s", *ctx.MarginRatio, limits.MinMarginRatio), nil}
	}
	return ok("maintenance_margin")
}

func ruleSlippage(ctx contracts.RiskContext, limits Limits) ruleResult {
	if ctx.EstimatedSlippageBPS != nil && ctx.EstimatedSlippageBPS.Cmp(limits.MaxSlippageBPS) > 0 {
		return ruleResult{"slippage", actionReject, fmt.Sprintf("预估滑点 %sbps > 上限 %sbps", *ctx.EstimatedSlippageBPS, limits.MaxSlippageBPS), nil}
	}
	return ok("slippage")
}

func rulePriceImpact(ctx contracts.RiskContext, limits Limits) ruleResult {
	if ctx.EstimatedPriceImpact != nil && ctx.EstimatedPriceImpact.Cmp(limits.MaxPriceImpact) > 0 {
		return ruleResult{"price_impact", actionReject, fmt.Sprintf("链上价格冲击 %s > 上限 %s", *ctx.EstimatedPriceImpact, limits.MaxPriceImpact), nil}
	}
	return ok("price_impact")
}

func ruleInfiniteApproval(proposal contracts.TradeProposal, ctx contracts.RiskContext) ruleResult {
	if !ctx.Onchain || !isOpen(proposal) {
		return ok("infinite_approval")
	}
	if ctx.ApprovalUnlimited {
		return ruleResult{"infinite_approval", actionReject, "检测到无限授权（unlimited approve），禁止", nil}
	}
	if ctx.ApprovalAmount != nil {
		cap := proposal.SizeQuote.Mul(contracts.MustDecimal("1.05"))
		if ctx.ApprovalAmount.Cmp(cap) > 0 {
			return ruleResult{"infinite_approval", actionReject, fmt.Sprintf("授权额度 %s 远超交易额 %s（须精确额度）", *ctx.ApprovalAmount, proposal.SizeQuote), nil}
		}
	}
	return ok("infinite_approval")
}

func ruleContractWhitelist(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if !ctx.Onchain || !isOpen(proposal) {
		return ok("contract_whitelist")
	}
	address := ""
	if ctx.ContractAddress != nil {
		address = strings.ToLower(strings.TrimSpace(*ctx.ContractAddress))
	}
	if _, allowed := limits.ContractWhitelist[address]; address == "" || !allowed {
		display := "未知"
		if ctx.ContractAddress != nil && *ctx.ContractAddress != "" {
			display = *ctx.ContractAddress
		}
		return ruleResult{"contract_whitelist", actionReject, fmt.Sprintf("合约 %s 不在白名单", display), nil}
	}
	return ok("contract_whitelist")
}

func ruleMinPoolTVL(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if ctx.Onchain && isOpen(proposal) && ctx.PoolTVLUSD != nil && ctx.PoolTVLUSD.Cmp(limits.MinPoolTVL) < 0 {
		return ruleResult{"min_pool_tvl", actionReject, fmt.Sprintf("池 TVL %s < 下限 %s", *ctx.PoolTVLUSD, limits.MinPoolTVL), nil}
	}
	return ok("min_pool_tvl")
}

func ruleGasCap(ctx contracts.RiskContext, limits Limits) ruleResult {
	if ctx.Onchain && ctx.EstimatedGasQuote != nil && ctx.EstimatedGasQuote.Cmp(limits.MaxGasQuote) > 0 {
		return ruleResult{"gas_cap", actionReject, fmt.Sprintf("gas 成本 %s > 上限 %s", *ctx.EstimatedGasQuote, limits.MaxGasQuote), nil}
	}
	return ok("gas_cap")
}

func ruleMEVRoute(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if ctx.Onchain && isOpen(proposal) && limits.RequirePrivateMempool && ctx.MEVProtected != nil && !*ctx.MEVProtected {
		return ruleResult{"mev_route", actionReject, "未走 MEV 防护路由（私有内存池），拒绝", nil}
	}
	return ok("mev_route")
}

func rulePerTradeRisk(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if !isOpen(proposal) || proposal.StopLoss == nil || !ctx.RefPrice.IsPositive() {
		return ok("per_trade_risk")
	}
	stopFraction := mustQuo(ctx.RefPrice.Sub(*proposal.StopLoss).Abs(), ctx.RefPrice)
	if !stopFraction.IsPositive() {
		return ruleResult{"per_trade_risk", actionReject, "止损距离为零，无法定风险", nil}
	}
	riskQuote := proposal.SizeQuote.Mul(stopFraction)
	budget := ctx.EquityQuote.Mul(limits.MaxRiskPerTrade)
	if riskQuote.Cmp(budget) > 0 {
		maxSize := mustQuo(budget, stopFraction)
		return ruleResult{"per_trade_risk", actionDownsize, fmt.Sprintf("单笔风险 %s > 预算 %s，缩仓至 %s", riskQuote, budget, maxSize), &maxSize}
	}
	return ok("per_trade_risk")
}

func rulePositionCap(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if !isOpen(proposal) {
		return ok("position_cap")
	}
	cap := ctx.EquityQuote.Mul(limits.MaxPositionPct)
	if proposal.SizeQuote.Cmp(cap) > 0 {
		return ruleResult{"position_cap", actionDownsize, fmt.Sprintf("单仓 %s > 上限 %s", proposal.SizeQuote, cap), &cap}
	}
	return ok("position_cap")
}

func ruleGrossExposure(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if !isOpen(proposal) {
		return ok("gross_exposure")
	}
	cap := ctx.EquityQuote.Mul(limits.MaxGrossExposure)
	room := cap.Sub(ctx.GrossExposureQuote)
	if !room.IsPositive() {
		return ruleResult{"gross_exposure", actionReject, fmt.Sprintf("总敞口已达上限 %s，无新增空间", cap), nil}
	}
	if proposal.SizeQuote.Cmp(room) > 0 {
		return ruleResult{"gross_exposure", actionDownsize, fmt.Sprintf("新增后超总敞口上限 %s，缩至剩余空间 %s", cap, room), &room}
	}
	return ok("gross_exposure")
}

func ruleSymbolConcentration(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if !isOpen(proposal) {
		return ok("symbol_concentration")
	}
	cap := ctx.EquityQuote.Mul(limits.MaxSymbolConcentration)
	room := cap.Sub(ctx.SymbolExposureQuote)
	if !room.IsPositive() {
		return ruleResult{"symbol_concentration", actionReject, fmt.Sprintf("该标的集中度已达上限 %s", cap), nil}
	}
	if proposal.SizeQuote.Cmp(room) > 0 {
		return ruleResult{"symbol_concentration", actionDownsize, fmt.Sprintf("超单标的集中度上限 %s，缩至 %s", cap, room), &room}
	}
	return ok("symbol_concentration")
}

func ruleCorrelatedExposure(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if !isOpen(proposal) || ctx.CorrelatedExposureQuote == nil {
		return ok("correlated_exposure")
	}
	cap := ctx.EquityQuote.Mul(limits.MaxCorrelatedExposure)
	room := cap.Sub(*ctx.CorrelatedExposureQuote)
	if !room.IsPositive() {
		return ruleResult{"correlated_exposure", actionReject, fmt.Sprintf("相关性簇同向敞口已达上限 %s", cap), nil}
	}
	if proposal.SizeQuote.Cmp(room) > 0 {
		return ruleResult{"correlated_exposure", actionDownsize, fmt.Sprintf("超相关性簇同向敞口上限 %s，缩至剩余 %s", cap, room), &room}
	}
	return ok("correlated_exposure")
}

func ruleCVAR(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits) ruleResult {
	if !isOpen(proposal) || ctx.PortfolioCVARQuote == nil {
		return ok("cvar_limit")
	}
	cap := ctx.EquityQuote.Mul(limits.MaxCVARPct)
	if ctx.PortfolioCVARQuote.Cmp(cap) > 0 {
		return ruleResult{"cvar_limit", actionReject, fmt.Sprintf("组合 CVaR %s > 上限 %s", *ctx.PortfolioCVARQuote, cap), nil}
	}
	return ok("cvar_limit")
}

func baseRiskScore(proposal contracts.TradeProposal, ctx contracts.RiskContext, limits Limits, size contracts.Decimal) float64 {
	if !isOpen(proposal) || !ctx.EquityQuote.IsPositive() {
		return 0
	}
	ratios := make([]contracts.Decimal, 0, 5)
	if limits.MaxLeverage.IsPositive() {
		leverage, err := contracts.ParseDecimal(strconv.FormatFloat(proposal.Leverage, 'g', -1, 64))
		if err == nil {
			ratios = append(ratios, mustQuo(leverage, limits.MaxLeverage))
		}
	}
	if limits.MaxPositionPct.IsPositive() {
		cap := ctx.EquityQuote.Mul(limits.MaxPositionPct)
		if cap.IsPositive() {
			ratios = append(ratios, mustQuo(size, cap))
		}
	}
	if proposal.StopLoss != nil && ctx.RefPrice.IsPositive() && limits.MaxRiskPerTrade.IsPositive() {
		stopFraction := mustQuo(ctx.RefPrice.Sub(*proposal.StopLoss).Abs(), ctx.RefPrice)
		budget := ctx.EquityQuote.Mul(limits.MaxRiskPerTrade)
		if budget.IsPositive() {
			ratios = append(ratios, mustQuo(size.Mul(stopFraction), budget))
		}
	}
	if limits.MaxGrossExposure.IsPositive() {
		cap := ctx.EquityQuote.Mul(limits.MaxGrossExposure)
		if cap.IsPositive() {
			ratios = append(ratios, mustQuo(ctx.GrossExposureQuote.Add(size), cap))
		}
	}
	if ctx.PortfolioCVARQuote != nil && limits.MaxCVARPct.IsPositive() {
		cap := ctx.EquityQuote.Mul(limits.MaxCVARPct)
		if cap.IsPositive() {
			ratios = append(ratios, mustQuo(*ctx.PortfolioCVARQuote, cap))
		}
	}
	maximum := 0.0
	for _, ratio := range ratios {
		value, err := ratio.Float64()
		if err == nil {
			maximum = math.Max(maximum, value)
		}
	}
	return math.Max(0, math.Min(1, maximum))
}

func mustQuo(numerator, denominator contracts.Decimal) contracts.Decimal {
	value, err := numerator.Quo(denominator)
	if err != nil {
		return contracts.Zero()
	}
	return value
}
