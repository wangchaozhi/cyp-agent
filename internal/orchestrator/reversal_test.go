package orchestrator

import (
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/riskstate"
)

func reversalFixture() (contracts.Position, contracts.TradeProposal, config.AutomationConfig) {
	position := contracts.Position{Symbol: "BTC/USDT", Side: contracts.SideShort, Instrument: contracts.InstrumentSpot}
	proposal := contracts.TradeProposal{Symbol: position.Symbol, Side: contracts.SideLong, Instrument: position.Instrument}
	settings := config.DefaultSettings().Automation
	settings.Enabled, settings.EntryEnabled, settings.ApprovalEnabled, settings.ReverseEnabled = true, true, true, true
	return position, proposal, settings
}

func TestReversalTrackerRequiresConsecutiveSignals(t *testing.T) {
	position, proposal, settings := reversalFixture()
	tracker := NewReversalTracker()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	first := tracker.Observe(position, proposal, now, settings, nil)
	second := tracker.Observe(position, proposal, now.Add(5*time.Minute), settings, nil)
	if first.Ready || first.Confirmations != 1 || !second.Ready || second.Confirmations != 2 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestReversalTrackerEnforcesCooldownAndDailyLimit(t *testing.T) {
	position, proposal, settings := reversalFixture()
	tracker := NewReversalTracker()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	trades := []riskstate.TradeRecord{{
		ClientID: "reverse-close-prior", Kind: "close", Symbol: position.Symbol,
		Instrument: position.Instrument, TS: now.Add(-10 * time.Minute),
	}}
	cooling := tracker.Observe(position, proposal, now, settings, trades)
	if cooling.Ready || cooling.CooldownUntil == nil || cooling.ReversalsToday != 1 {
		t.Fatalf("cooling decision=%+v", cooling)
	}
	settings.ReverseCooldownMins = 0
	settings.MaxReversalsPerDay = 1
	limited := tracker.Observe(position, proposal, now, settings, trades)
	if limited.Ready || limited.Reason != "已达到每日自动反向次数上限" {
		t.Fatalf("limited decision=%+v", limited)
	}
}
