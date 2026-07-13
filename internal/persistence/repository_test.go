package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemoryRepositoryConcurrentAndBounded(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository(25)
	ctx := context.Background()
	var wait sync.WaitGroup
	for index := 0; index < 50; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			runID := fmt.Sprintf("run-%d", index%5)
			if err := repository.SaveCheckpoint(ctx, runID, fmt.Sprintf("step-%d", index), map[string]any{
				"index": index,
			}); err != nil {
				t.Errorf("save checkpoint: %v", err)
			}
			if err := repository.AppendLessons(ctx, "BTC/USDT", []string{fmt.Sprintf("lesson-%d", index)}); err != nil {
				t.Errorf("append lesson: %v", err)
			}
		}()
	}
	wait.Wait()

	lessons, err := repository.GetLessons(ctx, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons) != 25 {
		t.Fatalf("bounded lessons = %d, want 25", len(lessons))
	}
	steps, err := repository.LoadCheckpoints(ctx, "run-0")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 10 {
		t.Fatalf("checkpoints = %d, want 10", len(steps))
	}

	for step := range steps {
		steps[step][0] = '['
		break
	}
	again, err := repository.LoadCheckpoints(ctx, "run-0")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range again {
		if !json.Valid(raw) {
			t.Fatal("caller mutation corrupted repository checkpoint")
		}
	}
}

func TestLessonRelevanceAndContextCancellation(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository(10)
	ctx := context.Background()
	if err := repository.AppendLessons(ctx, "ETH/USDT", []string{"ETH momentum"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AppendLessons(ctx, "BTC/USDT", []string{"BTC risk lesson", "BTC breakout"}); err != nil {
		t.Fatal(err)
	}
	lessons, err := repository.GetLessons(ctx, 1, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons) != 1 || lessons[0] != "BTC breakout" {
		t.Fatalf("relevant lessons = %v", lessons)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repository.SaveCheckpoint(canceled, "run", "step", map[string]any{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("save error = %v, want context canceled", err)
	}
}

func TestCheckpointSecretsAreMasked(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository(10)
	if err := repository.SaveCheckpoint(context.Background(), "run", "config", map[string]any{
		"db_url": "postgresql://user:password@host/db",
		"nested": map[string]any{"api_secret": "actual-secret", "safe": "visible"},
	}); err != nil {
		t.Fatal(err)
	}
	steps, err := repository.LoadCheckpoints(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	text := string(steps["config"])
	if text == "" || containsAny(text, "actual-secret", "postgresql://") {
		t.Fatalf("secret leaked into checkpoint: %s", text)
	}
	if !containsAny(text, "***") {
		t.Fatalf("mask missing from checkpoint: %s", text)
	}
}

func TestCheckpointBatchIsAtomicAndRunHistoryIsBounded(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository(10)
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	repository.now = func() time.Time { return now }
	ctx := context.Background()
	if err := repository.SaveCheckpoints(ctx, "run-1", map[string]any{
		"proposal": map[string]any{"ok": true}, "result": map[string]any{"status": "done"},
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := repository.LoadCheckpoints(ctx, "run-1")
	if err := repository.SaveCheckpoints(ctx, "run-1", map[string]any{
		"valid": true, "invalid": func() {},
	}); err == nil {
		t.Fatal("invalid checkpoint batch unexpectedly succeeded")
	}
	after, _ := repository.LoadCheckpoints(ctx, "run-1")
	if len(after) != len(before) {
		t.Fatalf("failed batch partially changed checkpoints: before=%v after=%v", before, after)
	}
	for index := 2; index <= 4; index++ {
		now = now.Add(time.Minute)
		if err := repository.SaveCheckpoint(ctx, fmt.Sprintf("run-%d", index), "result", index); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.SaveCheckpoint(ctx, "__runtime_preferences__", "watchlist", []string{"BTC/USDT"}); err != nil {
		t.Fatal(err)
	}
	removed, err := repository.PruneCheckpoints(ctx, 2)
	if err != nil || removed != 2 {
		t.Fatalf("prune removed=%d err=%v", removed, err)
	}
	for _, runID := range []string{"run-1", "run-2"} {
		steps, _ := repository.LoadCheckpoints(ctx, runID)
		if len(steps) != 0 {
			t.Fatalf("old run %s was retained: %v", runID, steps)
		}
	}
	protected, _ := repository.LoadCheckpoints(ctx, "__runtime_preferences__")
	if len(protected) != 1 {
		t.Fatalf("system checkpoint was pruned: %v", protected)
	}
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if len(value) > 0 && len(text) >= len(value) {
			for index := 0; index+len(value) <= len(text); index++ {
				if text[index:index+len(value)] == value {
					return true
				}
			}
		}
	}
	return false
}
