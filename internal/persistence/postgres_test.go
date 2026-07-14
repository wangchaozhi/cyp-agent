package persistence

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/orders"
)

func TestPostgresCheckpointBatchAndRetention(t *testing.T) {
	dsn := os.Getenv("CYP_TEST_PG_URL")
	if dsn == "" {
		t.Skip("CYP_TEST_PG_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	adminConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("cyp_persistence_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE")
	}()

	repository, err := NewPostgresRepository(ctx, postgresDSNWithSearchPath(dsn, schema), 20)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	for index := 1; index <= 4; index++ {
		runID := fmt.Sprintf("run-%d", index)
		if err := repository.SaveCheckpoints(ctx, runID, map[string]any{
			"proposal": map[string]any{"index": index}, "result": map[string]any{"ok": true},
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if err := repository.SaveCheckpoint(ctx, "__runtime_settings__", "settings", map[string]any{"scan": 600}); err != nil {
		t.Fatal(err)
	}
	removed, err := repository.PruneCheckpoints(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed runs = %d, want 2", removed)
	}
	for _, runID := range []string{"run-3", "run-4", "__runtime_settings__"} {
		loaded, err := repository.LoadCheckpoints(ctx, runID)
		if err != nil || len(loaded) == 0 {
			t.Fatalf("load %s after prune: checkpoints=%v err=%v", runID, loaded, err)
		}
	}
	intent := contracts.OrderIntent{
		ClientID: "pg-order", Symbol: "BTC/USDT", Venue: "paper",
		Side: contracts.SideLong, Instrument: contracts.InstrumentSpot,
		OrderType: contracts.EntryTypeMarket, SizeQuote: contracts.MustDecimal("100"),
	}
	if err := repository.AppendOrderEvent(ctx, orders.Event{
		EventID: "pg-event-open", ClientID: intent.ClientID, TS: time.Now().UTC(),
		Status: contracts.OrderStatusNew, Intent: &intent,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AppendOrderEvent(ctx, orders.Event{
		EventID: "pg-event-cancel", ClientID: intent.ClientID, TS: time.Now().UTC(),
		Status: contracts.OrderStatusCanceled,
	}); err != nil {
		t.Fatal(err)
	}
	events, err := repository.LoadOrderEvents(ctx)
	if err != nil || len(events) != 2 {
		t.Fatalf("PostgreSQL order events=%v err=%v", events, err)
	}
	journal, err := orders.Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	order, exists := journal.Get(intent.ClientID)
	if !exists || order.Status != contracts.OrderStatusCanceled {
		t.Fatalf("replayed PostgreSQL order=%+v exists=%v", order, exists)
	}

	second, err := NewPostgresRepository(ctx, postgresDSNWithSearchPath(dsn, schema), 20)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	scope := fmt.Sprintf("okx-live:test:%d", time.Now().UnixNano())
	if err := repository.AcquireExecutionLease(ctx, scope); err != nil {
		t.Fatalf("first execution lease: %v", err)
	}
	if err := repository.ValidateExecutionLease(ctx); err != nil {
		t.Fatalf("validate execution lease: %v", err)
	}
	if err := second.AcquireExecutionLease(ctx, scope); !errors.Is(err, ErrExecutionLeaseHeld) {
		t.Fatalf("second owner error = %v, want ErrExecutionLeaseHeld", err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.AcquireExecutionLease(ctx, scope); err != nil {
		t.Fatalf("lease was not released on close: %v", err)
	}
}

func postgresDSNWithSearchPath(dsn, schema string) string {
	if parsed, err := url.Parse(dsn); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
