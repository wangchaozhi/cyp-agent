package runtimeprefs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/persistence"
)

func TestWatchlistStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := New(persistence.NewMemoryRepository(10))
	if _, found, err := store.LoadWatchlist(ctx); err != nil || found {
		t.Fatalf("empty load: found=%v err=%v", found, err)
	}
	want := []string{"BTC/USDT:USDT", "ETH/USDT:USDT"}
	if err := store.SaveWatchlist(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.LoadWatchlist(ctx)
	if err != nil || !found || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("load: got=%#v found=%v err=%v", got, found, err)
	}
	got[0] = "mutated"
	reloaded, _, _ := store.LoadWatchlist(ctx)
	if reloaded[0] != want[0] {
		t.Fatal("loaded watchlist aliases durable state")
	}
}

func TestAutomationStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := New(persistence.NewMemoryRepository(10))
	want := config.DefaultSettings().Automation
	want.Enabled = true
	if err := store.SaveAutomation(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.LoadAutomation(ctx)
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if err != nil || !found || string(gotJSON) != string(wantJSON) {
		t.Fatalf("automation: got=%#v found=%v err=%v", got, found, err)
	}
}

func TestAutomationStoreMergesNewSafetyDefaultsIntoLegacySnapshot(t *testing.T) {
	ctx := context.Background()
	repository := persistence.NewMemoryRepository(10)
	if err := repository.SaveCheckpoint(ctx, checkpointRunID, automationStep, map[string]any{
		"enabled": false, "scan_enabled": true, "approval_enabled": true, "exit_enabled": true,
		"max_risk_score": 0.5, "max_quote": "200", "min_confidence": 0.65,
		"min_reward_risk": 1.5, "ewma_lambda": 0.94, "volatility_multiplier": 3,
		"trail_activation_r": 1, "trail_giveback_r": 0.5, "max_holding_minutes": 360,
		"time_stop_min_r": 0, "exit_confirmations": 2, "exit_min_samples": 8,
	}); err != nil {
		t.Fatal(err)
	}
	got, found, err := New(repository).LoadAutomation(ctx)
	if err != nil || !found {
		t.Fatalf("load found=%v err=%v", found, err)
	}
	if !got.EntryEnabled || !got.ReverseEnabled || !got.AddEnabled || got.KellyScale != 0.25 ||
		got.AddRiskDecay != 0.5 || got.ProfitTargetR != 1.5 || got.ReverseConfirmations != 2 ||
		got.MinEntryQuote.String() != "20" {
		t.Fatalf("legacy automation did not inherit safe defaults: %+v", got)
	}
}
