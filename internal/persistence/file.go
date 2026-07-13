package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/orders"
)

// FileRepository persists the complete repository state as one JSON document.
// Methods are safe for concurrent use within one process. Multi-process access
// is intentionally left to the future PostgreSQL adapter.
type FileRepository struct {
	mu         sync.RWMutex
	path       string
	state      repositoryState
	maxLessons int
	now        func() time.Time
}

func NewFileRepository(path string, maxLessons int) (*FileRepository, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("repository file path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve repository path: %w", err)
	}
	if err := recoverInterruptedReplace(absolute); err != nil {
		return nil, err
	}
	state, err := loadFileState(absolute)
	if err != nil {
		return nil, err
	}
	repository := &FileRepository{
		path: absolute, state: state,
		maxLessons: normalizeMaxLessons(maxLessons), now: time.Now,
	}
	if overflow := len(repository.state.Lessons) - repository.maxLessons; overflow > 0 {
		repository.state.Lessons = append([]lessonRecord(nil), repository.state.Lessons[overflow:]...)
	}
	return repository, nil
}

func loadFileState(path string) (repositoryState, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return newRepositoryState(), nil
	}
	if err != nil {
		return repositoryState{}, fmt.Errorf("read repository file: %w", err)
	}
	var state repositoryState
	if err := json.Unmarshal(raw, &state); err != nil {
		return repositoryState{}, fmt.Errorf("decode repository file: %w", err)
	}
	if state.Version < 0 || state.Version > 3 {
		return repositoryState{}, fmt.Errorf("unsupported repository version %d", state.Version)
	}
	state.Version = 3
	if state.Checkpoints == nil {
		state.Checkpoints = make(map[string]map[string]json.RawMessage)
	}
	if state.CheckpointUpdated == nil {
		state.CheckpointUpdated = make(map[string]time.Time)
	}
	if state.Lessons == nil {
		state.Lessons = make([]lessonRecord, 0)
	}
	if state.OrderEvents == nil {
		state.OrderEvents = make([]orders.Event, 0)
	}
	for runID, steps := range state.Checkpoints {
		if steps == nil {
			state.Checkpoints[runID] = make(map[string]json.RawMessage)
			continue
		}
		for step, checkpoint := range steps {
			if !json.Valid(checkpoint) {
				return repositoryState{}, fmt.Errorf("invalid checkpoint JSON at %s/%s", runID, step)
			}
		}
	}
	for _, lesson := range state.Lessons {
		if lesson.ID > state.NextLessonID {
			state.NextLessonID = lesson.ID
		}
	}
	// Validate the complete stream at startup. Replay performs the legal state
	// transition validation after the repository has loaded it.
	seenEvents := make(map[string]orders.Event, len(state.OrderEvents))
	for index, event := range state.OrderEvents {
		normalized, normalizeErr := normalizeOrderEvent(event)
		if normalizeErr != nil {
			return repositoryState{}, fmt.Errorf("invalid order event %d: %w", index, normalizeErr)
		}
		if existing, seen := seenEvents[normalized.EventID]; seen && !equivalentOrderEvent(existing, normalized) {
			return repositoryState{}, fmt.Errorf("conflicting duplicate order event %s", normalized.EventID)
		}
		seenEvents[normalized.EventID] = normalized
		state.OrderEvents[index] = normalized
	}
	return state, nil
}

func (repository *FileRepository) AppendOrderEvent(ctx context.Context, event orders.Event) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	return repository.mutate(ctx, func(state *repositoryState) error {
		return appendOrderEvent(state, event)
	})
}

func (repository *FileRepository) LoadOrderEvents(ctx context.Context) ([]orders.Event, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return loadOrderEvents(repository.state), nil
}

func (repository *FileRepository) SaveCheckpoint(
	ctx context.Context,
	runID, step string,
	value any,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	raw, err := encodeCheckpoint(value)
	if err != nil {
		return err
	}
	return repository.mutate(ctx, func(state *repositoryState) error {
		return saveCheckpoint(state, runID, step, raw, repository.now())
	})
}

func (repository *FileRepository) SaveCheckpoints(
	ctx context.Context,
	runID string,
	values map[string]any,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	encoded, err := encodeCheckpoints(values)
	if err != nil {
		return err
	}
	return repository.mutate(ctx, func(state *repositoryState) error {
		now := repository.now()
		for step, raw := range encoded {
			if err := saveCheckpoint(state, runID, step, raw, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (repository *FileRepository) LoadCheckpoints(
	ctx context.Context,
	runID string,
) (map[string]json.RawMessage, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return loadCheckpoints(repository.state, runID)
}

func (repository *FileRepository) PruneCheckpoints(ctx context.Context, keepRecentRuns int) (int, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	next := cloneState(repository.state)
	removed, err := pruneCheckpoints(&next, keepRecentRuns)
	if err != nil || removed == 0 {
		return removed, err
	}
	if err := writeStateAtomically(ctx, repository.path, next); err != nil {
		return 0, err
	}
	repository.state = next
	return removed, nil
}

func (repository *FileRepository) AppendLessons(
	ctx context.Context,
	symbol string,
	lessons []string,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	return repository.mutate(ctx, func(state *repositoryState) error {
		appendLessons(state, repository.maxLessons, symbol, lessons, repository.now())
		return nil
	})
}

func (repository *FileRepository) GetLessons(
	ctx context.Context,
	limit int,
	symbol string,
) ([]string, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return getLessons(repository.state, limit, symbol), nil
}

func (repository *FileRepository) mutate(
	ctx context.Context,
	operation func(*repositoryState) error,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}
	next := cloneState(repository.state)
	if err := operation(&next); err != nil {
		return err
	}
	if err := writeStateAtomically(ctx, repository.path, next); err != nil {
		return err
	}
	repository.state = next
	return nil
}

func writeStateAtomically(ctx context.Context, path string, state repositoryState) (err error) {
	if err := contextError(ctx); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create repository directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create repository temp file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure repository temp file: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		return fmt.Errorf("encode repository state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync repository temp file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close repository temp file: %w", err)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := replaceFileRecoverably(temporaryPath, path); err != nil {
		return err
	}
	if directoryHandle, openErr := os.Open(directory); openErr == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

// replaceFileRecoverably uses the direct atomic rename where supported. On
// Windows filesystems that reject replacing an existing destination, it keeps
// a fixed backup and restores it if the second rename fails. Startup recovery
// handles a process crash between those two renames.
func replaceFileRecoverably(temporaryPath, destination string) error {
	if err := recoverInterruptedReplace(destination); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err == nil {
		return nil
	}
	if _, err := os.Stat(destination); err != nil {
		return fmt.Errorf("replace repository file: %w", err)
	}
	backup := destination + ".bak"
	if _, err := os.Stat(backup); err == nil {
		return errors.New("repository backup already exists; reopen repository to recover it")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect repository backup: %w", err)
	}
	if err := os.Rename(destination, backup); err != nil {
		return fmt.Errorf("stage repository backup: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		restoreErr := os.Rename(backup, destination)
		if restoreErr != nil {
			return errors.Join(
				fmt.Errorf("install repository file: %w", err),
				fmt.Errorf("restore repository backup: %w", restoreErr),
			)
		}
		return fmt.Errorf("install repository file: %w", err)
	}
	// The new destination is already durable. A backup cleanup failure must not
	// make memory diverge from disk; the next mutation/startup retries cleanup.
	_ = os.Remove(backup)
	return nil
}

func recoverInterruptedReplace(destination string) error {
	backup := destination + ".bak"
	_, destinationErr := os.Stat(destination)
	_, backupErr := os.Stat(backup)
	switch {
	case backupErr == nil && errors.Is(destinationErr, fs.ErrNotExist):
		if err := os.Rename(backup, destination); err != nil {
			return fmt.Errorf("recover repository backup: %w", err)
		}
	case backupErr == nil && destinationErr == nil:
		if raw, err := os.ReadFile(destination); err != nil || !json.Valid(raw) {
			return errors.New("repository replacement interrupted and destination is invalid")
		}
		if err := os.Remove(backup); err != nil {
			return fmt.Errorf("finish repository recovery: %w", err)
		}
	case backupErr != nil && !errors.Is(backupErr, fs.ErrNotExist):
		return fmt.Errorf("inspect repository backup: %w", backupErr)
	}
	return nil
}

func (repository *FileRepository) Close() error { return nil }

var _ Repository = (*FileRepository)(nil)
