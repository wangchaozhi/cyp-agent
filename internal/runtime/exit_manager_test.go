package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

func TestAutomatedExitManagerHonorsMasterAndRequiresConfirmation(t *testing.T) {
	target := venue.NewPaperVenue()
	openPaperPosition(t, target, true)
	automation := config.DefaultSettings().Automation
	automation.Enabled = false
	automation.ExitEnabled = true
	automation.TrailActivationR = 0.2
	automation.TrailGivebackR = 0.1
	automation.VolatilityMultiplier = 0
	automation.ExitMinSamples = 2
	automation.ExitConfirmations = 1
	exits := 0
	manager, err := NewAutomatedExitManager(AutomatedExitConfig{
		Venue: target, Interval: time.Second, Automation: func() config.AutomationConfig { return automation },
		State: func() RuntimeState { return RuntimeState{Mode: "paper", ExecutionVenue: "paper"} },
		Exit: func(context.Context, contracts.Position, contracts.Decimal, ExitDecision) error {
			exits++
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CheckOnce(context.Background()); err != nil || exits != 0 || len(manager.model.series) != 0 {
		t.Fatalf("disabled manager exits=%d series=%d err=%v", exits, len(manager.model.series), err)
	}
	automation.Enabled = true
	for _, mark := range []string{"100", "105", "103"} {
		if err := target.SetMarkPrice("BTC/USDT", contracts.MustDecimal(mark)); err != nil {
			t.Fatal(err)
		}
		if err := manager.CheckOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if exits != 1 {
		t.Fatalf("automated exits=%d want=1", exits)
	}
	automation.Enabled = false
	if err := manager.CheckOnce(context.Background()); err != nil || len(manager.model.series) != 0 {
		t.Fatalf("disable reset series=%d err=%v", len(manager.model.series), err)
	}
}

func TestAutomatedExitManagerRefusesLiveMode(t *testing.T) {
	target := venue.NewPaperVenue()
	openPaperPosition(t, target, true)
	automation := config.DefaultSettings().Automation
	automation.Enabled = true
	manager, err := NewAutomatedExitManager(AutomatedExitConfig{
		Venue: target, Interval: time.Second, Automation: func() config.AutomationConfig { return automation },
		State: func() RuntimeState { return RuntimeState{Mode: "live", ExecutionVenue: "paper"} },
		Exit: func(context.Context, contracts.Position, contracts.Decimal, ExitDecision) error {
			t.Fatal("live mode attempted an automated exit")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CheckOnce(context.Background()); !errors.Is(err, ErrLiveExecutionDisabled) {
		t.Fatalf("live mode error=%v", err)
	}
}
