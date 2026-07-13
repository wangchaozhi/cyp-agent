package orchestrator

import (
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func TestEvaluateAutoApprovalUsesFractionalKellyPositionSize(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Automation.Enabled = true
	settings.Automation.EntryEnabled = true
	settings.Automation.ApprovalEnabled = true
	settings.Automation.MaxRiskScore = 1
	settings.Automation.MaxQuote = contracts.MustDecimal("10000")
	settings.Automation.MinEntryQuote = contracts.MustDecimal("1")
	settings.Automation.MinConfidence = 0
	settings.Automation.MinRewardRisk = 1
	settings.AutoSymbols = "BTC/USDT"
	entry := contracts.MustDecimal("100")
	stop := contracts.MustDecimal("90")
	proposal := contracts.TradeProposal{
		Symbol: "BTC/USDT", Side: contracts.SideLong, SizeQuote: contracts.MustDecimal("5000"),
		Entry: contracts.PricePlan{Price: &entry}, StopLoss: &stop,
		TakeProfit: contracts.List[contracts.Decimal]{contracts.MustDecimal("130")}, Confidence: 0.6,
	}
	metrics := evaluateAutoApproval(settings, proposal, contracts.RiskAssessment{}, contracts.MustDecimal("10000"))
	if !metrics.Allowed {
		t.Fatalf("metrics rejected: %+v", metrics)
	}
	// Max risk is 1% of 10,000 and the stop is 10% away: 100 / 0.10 = 1,000.
	if metrics.RecommendedQuote.Cmp(contracts.MustDecimal("1000")) != 0 {
		t.Fatalf("recommended quote = %s, want 1000", metrics.RecommendedQuote)
	}
	if metrics.RiskFraction != 0.01 {
		t.Fatalf("risk fraction = %v, want 0.01", metrics.RiskFraction)
	}
}

func TestEvaluateAutoApprovalRequiresAutomaticEntrySwitch(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Automation.Enabled = true
	settings.Automation.EntryEnabled = false
	entry := contracts.MustDecimal("100")
	stop := contracts.MustDecimal("90")
	proposal := contracts.TradeProposal{
		Symbol: "BTC/USDT", Side: contracts.SideLong, SizeQuote: contracts.MustDecimal("100"),
		Entry: contracts.PricePlan{Price: &entry}, StopLoss: &stop,
		TakeProfit: contracts.List[contracts.Decimal]{contracts.MustDecimal("120")}, Confidence: 0.9,
	}
	metrics := evaluateAutoApproval(settings, proposal, contracts.RiskAssessment{}, contracts.MustDecimal("10000"))
	if metrics.Allowed || metrics.Reason != "自动审批已关闭" {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}
