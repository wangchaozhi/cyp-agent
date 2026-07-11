// Package persistence defines the durable state boundary used by the runtime
// and orchestrator. PostgreSQL adapters can implement Repository without
// changing callers; the first Go release ships in-memory and atomic JSON-file
// implementations only.
package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

const defaultMaxLessons = 200

var (
	ErrInvalidRunID = errors.New("run_id is required")
	ErrInvalidStep  = errors.New("checkpoint step is required")
)

// CheckpointRepository stores JSON-safe run checkpoints. Returned RawMessages
// are owned by the caller and may be modified safely.
type CheckpointRepository interface {
	SaveCheckpoint(ctx context.Context, runID, step string, value any) error
	LoadCheckpoints(ctx context.Context, runID string) (map[string]json.RawMessage, error)
}

// LessonRepository stores bounded, symbol-aware lessons in chronological order.
type LessonRepository interface {
	AppendLessons(ctx context.Context, symbol string, lessons []string) error
	GetLessons(ctx context.Context, limit int, symbol string) ([]string, error)
}

// Repository is the adapter boundary reserved for a future PostgreSQL
// implementation. Close is intentionally present even though the bundled
// repositories do not retain external resources.
type Repository interface {
	CheckpointRepository
	LessonRepository
	Close() error
}

type lessonRecord struct {
	ID        uint64    `json:"id"`
	Symbol    string    `json:"symbol"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type repositoryState struct {
	Version      int                                   `json:"version"`
	NextLessonID uint64                                `json:"next_lesson_id"`
	Checkpoints  map[string]map[string]json.RawMessage `json:"checkpoints"`
	Lessons      []lessonRecord                        `json:"lessons"`
}

func newRepositoryState() repositoryState {
	return repositoryState{
		Version:     1,
		Checkpoints: make(map[string]map[string]json.RawMessage),
		Lessons:     make([]lessonRecord, 0),
	}
}

func normalizeMaxLessons(maxLessons int) int {
	if maxLessons <= 0 {
		return defaultMaxLessons
	}
	return maxLessons
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func encodeCheckpoint(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode checkpoint: %w", err)
	}
	sanitized, err := sanitizeCheckpoint(raw)
	if err != nil {
		return nil, fmt.Errorf("sanitize checkpoint: %w", err)
	}
	return sanitized, nil
}

// sanitizeCheckpoint recursively masks configuration and credential fields so
// neither the in-memory snapshot nor the JSON file can become a secret store.
func sanitizeCheckpoint(raw []byte) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	value = sanitizeValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

var sensitiveCheckpointHints = []string{
	"api_key", "apikey", "api_secret", "secret", "private_key", "mnemonic",
	"password", "token", "authorization", "db_url", "database_url", "dsn",
}

func sensitiveCheckpointKey(key string) bool {
	key = strings.ToLower(key)
	for _, hint := range sensitiveCheckpointHints {
		if strings.Contains(key, hint) {
			return true
		}
	}
	return false
}

func sanitizeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveCheckpointKey(key) {
				clean[key] = "***"
				continue
			}
			clean[key] = sanitizeValue(item)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for index, item := range typed {
			clean[index] = sanitizeValue(item)
		}
		return clean
	default:
		return value
	}
}

func saveCheckpoint(state *repositoryState, runID, step string, raw json.RawMessage) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ErrInvalidRunID
	}
	step = strings.TrimSpace(step)
	if step == "" {
		return ErrInvalidStep
	}
	steps := state.Checkpoints[runID]
	if steps == nil {
		steps = make(map[string]json.RawMessage)
		state.Checkpoints[runID] = steps
	}
	steps[step] = cloneRaw(raw)
	return nil
}

func loadCheckpoints(state repositoryState, runID string) (map[string]json.RawMessage, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, ErrInvalidRunID
	}
	steps := state.Checkpoints[runID]
	result := make(map[string]json.RawMessage, len(steps))
	for step, raw := range steps {
		result[step] = cloneRaw(raw)
	}
	return result, nil
}

func appendLessons(state *repositoryState, maxLessons int, symbol string, lessons []string, now time.Time) {
	symbol = strings.TrimSpace(symbol)
	for _, lesson := range lessons {
		lesson = strings.TrimSpace(lesson)
		if lesson == "" {
			continue
		}
		state.NextLessonID++
		state.Lessons = append(state.Lessons, lessonRecord{
			ID: state.NextLessonID, Symbol: symbol, Text: lesson, CreatedAt: now.UTC(),
		})
	}
	if overflow := len(state.Lessons) - maxLessons; overflow > 0 {
		trimmed := make([]lessonRecord, len(state.Lessons)-overflow)
		copy(trimmed, state.Lessons[overflow:])
		state.Lessons = trimmed
	}
}

func getLessons(state repositoryState, limit int, symbol string) []string {
	if limit <= 0 || len(state.Lessons) == 0 {
		return []string{}
	}
	if limit > len(state.Lessons) {
		limit = len(state.Lessons)
	}
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		start := len(state.Lessons) - limit
		result := make([]string, 0, limit)
		for _, lesson := range state.Lessons[start:] {
			result = append(result, lesson.Text)
		}
		return result
	}

	type scoredLesson struct {
		score float64
		item  lessonRecord
	}
	queryTokens := tokens(symbol)
	scored := make([]scoredLesson, 0, len(state.Lessons))
	for index, lesson := range state.Lessons {
		score := 0.0
		if lesson.Symbol != "" && lesson.Symbol == symbol {
			score += 2
		}
		for token := range tokens(lesson.Symbol + " " + lesson.Text) {
			if _, ok := queryTokens[token]; ok {
				score += 0.5
			}
		}
		score += float64(index) * 1e-6
		scored = append(scored, scoredLesson{score: score, item: lesson})
	}
	sort.SliceStable(scored, func(left, right int) bool {
		if scored[left].score == scored[right].score {
			return scored[left].item.ID > scored[right].item.ID
		}
		return scored[left].score > scored[right].score
	})
	scored = scored[:limit]
	sort.Slice(scored, func(left, right int) bool { return scored[left].item.ID < scored[right].item.ID })
	result := make([]string, 0, len(scored))
	for _, lesson := range scored {
		result = append(result, lesson.item.Text)
	}
	return result
}

func tokens(text string) map[string]struct{} {
	parts := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	result := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		if len([]rune(part)) > 1 {
			result[part] = struct{}{}
		}
	}
	return result
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func cloneState(state repositoryState) repositoryState {
	copyState := newRepositoryState()
	copyState.Version = state.Version
	copyState.NextLessonID = state.NextLessonID
	for runID, steps := range state.Checkpoints {
		copySteps := make(map[string]json.RawMessage, len(steps))
		for step, raw := range steps {
			copySteps[step] = cloneRaw(raw)
		}
		copyState.Checkpoints[runID] = copySteps
	}
	copyState.Lessons = append(copyState.Lessons, state.Lessons...)
	return copyState
}
