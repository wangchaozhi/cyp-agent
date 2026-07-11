package risk

import (
	"strings"
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func baseProposal() contracts.TradeProposal {
	stop := contracts.MustDecimal("95")
	return contracts.TradeProposal{
		Symbol: "BTC/USDT", Venue: "paper", Side: contracts.SideLong,
		Instrument: contracts.InstrumentSpot, SizeQuote: contracts.MustDecimal("1000"),
		Leverage: 1, MarginMode: contracts.MarginModeIsolated,
		Entry: contracts.PricePlan{Type: contracts.EntryTypeMarket}, StopLoss: &stop,
		TakeProfit: contracts.List[contracts.Decimal]{contracts.MustDecimal("110")},
		Confidence: 0.8, SupportingReports: contracts.List[string]{},
	}
}

func baseContext() contracts.RiskContext {
	return contracts.RiskContext{
		EquityQuote: contracts.MustDecimal("10000"), RefPrice: contracts.MustDecimal("100"),
		GrossExposureQuote: contracts.Zero(), SymbolExposureQuote: contracts.Zero(),
		DailyDrawdown: contracts.Zero(), WeeklyDrawdown: contracts.Zero(), TotalDrawdown: contracts.Zero(),
	}
}

func TestAssessCriticalRejections(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*contracts.TradeProposal, *contracts.RiskContext, *Limits)
		wantRule string
	}{
		{"kill switch", func(_ *contracts.TradeProposal, c *contracts.RiskContext, _ *Limits) { c.Kill = true }, "kill_switch"},
		{"reconciling", func(_ *contracts.TradeProposal, c *contracts.RiskContext, _ *Limits) { c.Reconciling = true }, "reconciling"},
		{"missing stop", func(p *contracts.TradeProposal, _ *contracts.RiskContext, _ *Limits) { p.StopLoss = nil }, "stop_loss_required"},
		{"bad stop direction", func(p *contracts.TradeProposal, _ *contracts.RiskContext, _ *Limits) {
			v := contracts.MustDecimal("101")
			p.StopLoss = &v
		}, "stop_loss_required"},
		{"rate limit", func(_ *contracts.TradeProposal, c *contracts.RiskContext, l *Limits) {
			c.OrdersLastHour = l.MaxOrdersPerHour
		}, "order_rate"},
		{"drawdown", func(_ *contracts.TradeProposal, c *contracts.RiskContext, l *Limits) {
			c.DailyDrawdown = l.DailyDrawdownLimit
		}, "drawdown_circuit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proposal, ctx, limits := baseProposal(), baseContext(), DefaultLimits()
			tt.mutate(&proposal, &ctx, &limits)
			assessment := Assess(proposal, ctx, limits)
			if assessment.Verdict != contracts.VerdictRejected {
				t.Fatalf("verdict = %s, want rejected", assessment.Verdict)
			}
			joined := strings.Join(assessment.HardViolations, "\n")
			if !strings.Contains(joined, tt.wantRule+":") {
				t.Fatalf("violations %q do not contain %s", joined, tt.wantRule)
			}
		})
	}
}

func TestAssessDownsizesToTightestExactCap(t *testing.T) {
	proposal := baseProposal()
	proposal.SizeQuote = contracts.MustDecimal("5000")
	ctx := baseContext()
	assessment := Assess(proposal, ctx, DefaultLimits())
	if assessment.Verdict != contracts.VerdictDownsized || assessment.AdjustedSizeQuote == nil {
		t.Fatalf("unexpected assessment: %#v", assessment)
	}
	// 5%% stop and 1%% risk budget => max size 2000, also equal to position cap.
	if got := assessment.AdjustedSizeQuote.String(); got != "2000" {
		t.Fatalf("adjusted size = %s, want 2000", got)
	}
}

func TestAssessApprovesSafeProposal(t *testing.T) {
	assessment := Assess(baseProposal(), baseContext(), DefaultLimits())
	if assessment.Verdict != contracts.VerdictApproved || len(assessment.HardViolations) != 0 {
		t.Fatalf("unexpected assessment: %#v", assessment)
	}
	if assessment.RiskScore <= 0 || assessment.RiskScore > 1 {
		t.Fatalf("risk score = %v", assessment.RiskScore)
	}
}
