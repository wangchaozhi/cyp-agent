package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestFileRepositoryPersistsAndReloadsAtomically(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state", "memory.json")
	repository, err := NewFileRepository(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var wait sync.WaitGroup
	for index := 0; index < 12; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := repository.SaveCheckpoint(ctx, "run", "step-"+string(rune('a'+index)), map[string]any{
				"value": index,
			}); err != nil {
				t.Errorf("save checkpoint: %v", err)
			}
		}()
	}
	wait.Wait()
	if err := repository.AppendLessons(ctx, "BTC/USDT", []string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatal("repository file is not valid JSON")
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected backup after successful replace: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".memory.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files leaked: %v", matches)
	}

	reloaded, err := NewFileRepository(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := reloaded.LoadCheckpoints(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 12 {
		t.Fatalf("reloaded checkpoints = %d, want 12", len(steps))
	}
	lessons, err := reloaded.GetLessons(ctx, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons) != 2 || lessons[0] != "one" || lessons[1] != "two" {
		t.Fatalf("reloaded lessons = %v", lessons)
	}
}

func TestFileRepositoryRecoversBackupAndPreservesOldSnapshotOnEncodeFailure(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	repository, err := NewFileRepository(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveCheckpoint(context.Background(), "run", "good", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveCheckpoint(context.Background(), "run", "bad", map[string]any{"bad": func() {}}); err == nil {
		t.Fatal("non-JSON checkpoint unexpectedly succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed mutation changed durable snapshot")
	}

	backup := path + ".bak"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewFileRepository(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := recovered.LoadCheckpoints(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := steps["good"]; !ok {
		t.Fatal("backup recovery lost checkpoint")
	}

	// Simulate a crash after the new destination was installed but before the
	// old backup was removed. Reopen keeps the valid destination and cleans up.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, current, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileRepository(path, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale backup was not cleaned: %v", err)
	}
}
