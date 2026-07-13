package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func TestExitModelTriggersConfirmedVolatilityTrail(t *testing.T) {
	settings := config.DefaultSettings().Automation
	settings.ExitMinSamples = 2
	settings.ExitConfirmations = 2
	settings.VolatilityMultiplier = 0
	settings.ProfitTargetR = 10
	position := contracts.Position{
		Symbol: "BTC/USDT:USDT", Venue: "okx", Side: contracts.SideLong,
		Instrument: contracts.InstrumentPerp, EntryPrice: contracts.MustDecimal("100"),
	}
	model := NewExitModel()
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	prices := []string{"100", "103", "101.4", "101.3"}
	var decision ExitDecision
	for index, price := range prices {
		decision = model.Observe(ExitObservation{
			Position: position, Mark: contracts.MustDecimal(price), StopLoss: contracts.MustDecimal("98"),
			OpenedAt: now.Add(-time.Hour), Now: now.Add(time.Duration(index) * time.Minute),
		}, settings)
	}
	if !decision.Trigger || decision.Confirmations != 2 || decision.PeakR != 1.5 {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestExitModelTimeStopAndReset(t *testing.T) {
	settings := config.DefaultSettings().Automation
	settings.MaxHoldingMinutes = 60
	settings.ExitConfirmations = 1
	position := contracts.Position{
		Symbol: "ETH/USDT:USDT", Venue: "okx", Side: contracts.SideShort,
		Instrument: contracts.InstrumentPerp, EntryPrice: contracts.MustDecimal("100"),
	}
	model := NewExitModel()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	decision := model.Observe(ExitObservation{
		Position: position, Mark: contracts.MustDecimal("101"), StopLoss: contracts.MustDecimal("102"),
		OpenedAt: now.Add(-2 * time.Hour), Now: now,
	}, settings)
	if !decision.Trigger || decision.CurrentR != -0.5 {
		t.Fatalf("decision=%+v", decision)
	}
	model.Reset()
	if len(model.series) != 0 {
		t.Fatal("reset retained position state")
	}
}

func TestExitModelClosesAtIdealProfitAndAdverseMarket(t *testing.T) {
	settings := config.DefaultSettings().Automation
	settings.ExitConfirmations = 1
	position := contracts.Position{
		Symbol: "SOL/USDT:USDT", Venue: "okx", Side: contracts.SideLong,
		Instrument: contracts.InstrumentPerp, EntryPrice: contracts.MustDecimal("100"),
	}
	model := NewExitModel()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	profit := model.Observe(ExitObservation{
		Position: position, Mark: contracts.MustDecimal("103"), StopLoss: contracts.MustDecimal("98"), Now: now,
	}, settings)
	if !profit.Trigger || !strings.Contains(profit.Reason, "理想收益") {
		t.Fatalf("ideal-profit exit=%+v", profit)
	}
	model.Remove(position)
	loss := model.Observe(ExitObservation{
		Position: position, Mark: contracts.MustDecimal("99"), StopLoss: contracts.MustDecimal("98"), Now: now,
	}, settings)
	if !loss.Trigger || !strings.Contains(loss.Reason, "行情恶化") {
		t.Fatalf("adverse-market exit=%+v", loss)
	}
}
