package persistence

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// MemoryRepository is a concurrency-safe process-local Repository.
type MemoryRepository struct {
	mu         sync.RWMutex
	state      repositoryState
	maxLessons int
	now        func() time.Time
}

func NewMemoryRepository(maxLessons int) *MemoryRepository {
	return &MemoryRepository{
		state:      newRepositoryState(),
		maxLessons: normalizeMaxLessons(maxLessons),
		now:        time.Now,
	}
}

func (repository *MemoryRepository) SaveCheckpoint(
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
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}
	return saveCheckpoint(&repository.state, runID, step, raw, repository.now())
}

func (repository *MemoryRepository) SaveCheckpoints(
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
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}
	now := repository.now()
	next := cloneState(repository.state)
	for step, raw := range encoded {
		if err := saveCheckpoint(&next, runID, step, raw, now); err != nil {
			return err
		}
	}
	repository.state = next
	return nil
}

func (repository *MemoryRepository) LoadCheckpoints(
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

func (repository *MemoryRepository) PruneCheckpoints(ctx context.Context, keepRecentRuns int) (int, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	next := cloneState(repository.state)
	removed, err := pruneCheckpoints(&next, keepRecentRuns)
	if err != nil {
		return 0, err
	}
	if removed > 0 {
		repository.state = next
	}
	return removed, nil
}

func (repository *MemoryRepository) AppendLessons(
	ctx context.Context,
	symbol string,
	lessons []string,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}
	appendLessons(&repository.state, repository.maxLessons, symbol, lessons, repository.now())
	return nil
}

func (repository *MemoryRepository) GetLessons(
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

func (repository *MemoryRepository) Close() error { return nil }

var _ Repository = (*MemoryRepository)(nil)
