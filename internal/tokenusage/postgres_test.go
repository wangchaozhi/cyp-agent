package tokenusage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wangchaozhi/cyp-agent/internal/llm"
)

func TestPostgresStorePersistsIdempotentDetailAndDailyAggregate(t *testing.T) {
	dsn := os.Getenv("CYP_TEST_PG_URL")
	if dsn == "" {
		t.Skip("CYP_TEST_PG_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	event := llm.UsageEvent{
		ID: "integration-token-usage", TS: time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC),
		RunID: "integration-run", Symbol: "BTC/USDT", Agent: "strategist", Source: "automatic",
		Provider: "integration-provider", Model: "integration-model", Operation: "text", Status: "success",
		InputTokens: 12, OutputTokens: 3, CostUSD: 0.01, DurationMS: 25,
	}
	cleanup, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup.Close()
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = cleanup.Exec(cleanupContext, "DELETE FROM llm_usage_events WHERE id=$1", event.ID)
		_, _ = cleanup.Exec(cleanupContext, "DELETE FROM llm_usage_daily WHERE provider=$1", event.Provider)
	}()

	for range 2 {
		if err := store.Save(ctx, event, "2099-01-02", "UTC"); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.Load(ctx, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range loaded {
		if item.ID == event.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("detail count = %d, want 1", count)
	}
	var calls int
	if err := cleanup.QueryRow(ctx, "SELECT calls FROM llm_usage_daily WHERE day='2099-01-02' AND provider=$1", event.Provider).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("daily calls = %d, want 1", calls)
	}
}
