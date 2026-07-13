// Package runtimeprefs persists non-secret dashboard preferences separately
// from environment-owned credentials and execution configuration.
package runtimeprefs

import (
	"context"
	"encoding/json"
)

const (
	checkpointRunID = "__runtime_preferences__"
	watchlistStep   = "watchlist"
)

type Repository interface {
	SaveCheckpoint(context.Context, string, string, any) error
	LoadCheckpoints(context.Context, string) (map[string]json.RawMessage, error)
}

type Store struct {
	repository Repository
}

func New(repository Repository) *Store {
	return &Store{repository: repository}
}

func (store *Store) LoadWatchlist(ctx context.Context) ([]string, bool, error) {
	if store == nil || store.repository == nil {
		return nil, false, nil
	}
	checkpoints, err := store.repository.LoadCheckpoints(ctx, checkpointRunID)
	if err != nil {
		return nil, false, err
	}
	raw := checkpoints[watchlistStep]
	if len(raw) == 0 {
		return nil, false, nil
	}
	var watchlist []string
	if err := json.Unmarshal(raw, &watchlist); err != nil {
		return nil, false, err
	}
	return append([]string(nil), watchlist...), true, nil
}

func (store *Store) SaveWatchlist(ctx context.Context, watchlist []string) error {
	if store == nil || store.repository == nil {
		return nil
	}
	return store.repository.SaveCheckpoint(
		ctx, checkpointRunID, watchlistStep, append([]string(nil), watchlist...),
	)
}
