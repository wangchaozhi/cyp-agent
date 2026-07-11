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
	return saveCheckpoint(&repository.state, runID, step, raw)
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
