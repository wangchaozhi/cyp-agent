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
