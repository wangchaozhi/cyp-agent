// Package persistence defines the durable state boundary used by agents,
// runtime preferences and the orchestrator. Memory, atomic JSON-file and
// PostgreSQL implementations share the same checkpoint contract.
package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/wangchaozhi/cyp-agent/internal/orders"
)

const (
	defaultMaxLessons        = 200
	defaultMaxOrderEvents    = 20_000
	defaultMaxTerminalOrders = 5_000
)

var (
	ErrInvalidRunID      = errors.New("run_id is required")
	ErrInvalidStep       = errors.New("checkpoint step is required")
	ErrInvalidKeep       = errors.New("checkpoint retention must be positive")
	ErrInvalidOrderEvent = errors.New("order event requires event_id, client_id, status and timestamp")
)

// CheckpointRepository stores JSON-safe run checkpoints. Returned RawMessages
// are owned by the caller and may be modified safely.
type CheckpointRepository interface {
	SaveCheckpoint(ctx context.Context, runID, step string, value any) error
	SaveCheckpoints(ctx context.Context, runID string, values map[string]any) error
	LoadCheckpoints(ctx context.Context, runID string) (map[string]json.RawMessage, error)
	PruneCheckpoints(ctx context.Context, keepRecentRuns int) (int, error)
}

// LessonRepository stores bounded, symbol-aware lessons in chronological order.
type LessonRepository interface {
	AppendLessons(ctx context.Context, symbol string, lessons []string) error
	GetLessons(ctx context.Context, limit int, symbol string) ([]string, error)
}

// Repository is the storage adapter boundary. Close releases retained file or
// database resources and is safe for repositories that retain none.
type Repository interface {
	CheckpointRepository
	LessonRepository
	orders.EventStore
	Close() error
}

// ExecutionLeaser provides exclusive ownership of a real execution account.
// Implementations must bind the lease to a live storage session and fail
// validation when that session is lost.
type ExecutionLeaser interface {
	AcquireExecutionLease(context.Context, string) error
	ValidateExecutionLease(context.Context) error
}

type lessonRecord struct {
	ID        uint64    `json:"id"`
	Symbol    string    `json:"symbol"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type repositoryState struct {
	Version           int                                   `json:"version"`
	NextLessonID      uint64                                `json:"next_lesson_id"`
	Checkpoints       map[string]map[string]json.RawMessage `json:"checkpoints"`
	CheckpointUpdated map[string]time.Time                  `json:"checkpoint_updated,omitempty"`
	Lessons           []lessonRecord                        `json:"lessons"`
	OrderEvents       []orders.Event                        `json:"order_events,omitempty"`
}

func newRepositoryState() repositoryState {
	return repositoryState{
		Version:           3,
		Checkpoints:       make(map[string]map[string]json.RawMessage),
		CheckpointUpdated: make(map[string]time.Time),
		Lessons:           make([]lessonRecord, 0),
		OrderEvents:       make([]orders.Event, 0),
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

func encodeCheckpoints(values map[string]any) (map[string]json.RawMessage, error) {
	if len(values) == 0 {
		return nil, ErrInvalidStep
	}
	encoded := make(map[string]json.RawMessage, len(values))
	for step, value := range values {
		step = strings.TrimSpace(step)
		if step == "" {
			return nil, ErrInvalidStep
		}
		raw, err := encodeCheckpoint(value)
		if err != nil {
			return nil, fmt.Errorf("checkpoint %s: %w", step, err)
		}
		encoded[step] = raw
	}
	return encoded, nil
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

func saveCheckpoint(state *repositoryState, runID, step string, raw json.RawMessage, now time.Time) error {
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
	if state.CheckpointUpdated == nil {
		state.CheckpointUpdated = make(map[string]time.Time)
	}
	steps[step] = cloneRaw(raw)
	state.CheckpointUpdated[runID] = now.UTC()
	return nil
}

func pruneCheckpoints(state *repositoryState, keepRecentRuns int) (int, error) {
	if keepRecentRuns <= 0 {
		return 0, ErrInvalidKeep
	}
	type candidate struct {
		runID   string
		updated time.Time
	}
	candidates := make([]candidate, 0, len(state.Checkpoints))
	for runID := range state.Checkpoints {
		if strings.HasPrefix(runID, "__") {
			continue
		}
		candidates = append(candidates, candidate{runID: runID, updated: state.CheckpointUpdated[runID]})
	}
	if len(candidates) <= keepRecentRuns {
		return 0, nil
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].updated.Equal(candidates[right].updated) {
			return candidates[left].runID < candidates[right].runID
		}
		return candidates[left].updated.Before(candidates[right].updated)
	})
	remove := len(candidates) - keepRecentRuns
	for _, item := range candidates[:remove] {
		delete(state.Checkpoints, item.runID)
		delete(state.CheckpointUpdated, item.runID)
	}
	return remove, nil
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
	for runID, updated := range state.CheckpointUpdated {
		copyState.CheckpointUpdated[runID] = updated
	}
	for runID, steps := range state.Checkpoints {
		copySteps := make(map[string]json.RawMessage, len(steps))
		for step, raw := range steps {
			copySteps[step] = cloneRaw(raw)
		}
		copyState.Checkpoints[runID] = copySteps
	}
	copyState.Lessons = append(copyState.Lessons, state.Lessons...)
	copyState.OrderEvents = make([]orders.Event, 0, len(state.OrderEvents))
	for _, event := range state.OrderEvents {
		copyState.OrderEvents = append(copyState.OrderEvents, cloneOrderEvent(event))
	}
	return copyState
}

func normalizeOrderEvent(event orders.Event) (orders.Event, error) {
	if strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.ClientID) == "" ||
		strings.TrimSpace(string(event.Status)) == "" || event.TS.IsZero() {
		return orders.Event{}, ErrInvalidOrderEvent
	}
	// Reuse the recursive secret masker and JSON round-trip so the repository
	// owns every nested slice/pointer and can never retain caller mutations.
	raw, err := json.Marshal(event)
	if err != nil {
		return orders.Event{}, fmt.Errorf("encode order event: %w", err)
	}
	raw, err = sanitizeCheckpoint(raw)
	if err != nil {
		return orders.Event{}, fmt.Errorf("sanitize order event: %w", err)
	}
	var normalized orders.Event
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return orders.Event{}, fmt.Errorf("decode order event: %w", err)
	}
	normalized.EventID = strings.TrimSpace(normalized.EventID)
	normalized.ClientID = strings.TrimSpace(normalized.ClientID)
	normalized.Note = strings.TrimSpace(normalized.Note)
	if len(normalized.Note) > 1000 {
		normalized.Note = normalized.Note[:1000]
	}
	normalized.TS = normalized.TS.UTC()
	return normalized, nil
}

func appendOrderEvent(state *repositoryState, event orders.Event) error {
	normalized, err := normalizeOrderEvent(event)
	if err != nil {
		return err
	}
	for _, existing := range state.OrderEvents {
		if existing.EventID != normalized.EventID {
			continue
		}
		if equivalentOrderEvent(existing, normalized) {
			return nil
		}
		return fmt.Errorf("order event id %s conflicts with persisted content", normalized.EventID)
	}
	state.OrderEvents = append(state.OrderEvents, normalized)
	compactOrderEvents(state, defaultMaxOrderEvents)
	return nil
}

func loadOrderEvents(state repositoryState) []orders.Event {
	result := make([]orders.Event, 0, len(state.OrderEvents))
	for _, event := range state.OrderEvents {
		result = append(result, cloneOrderEvent(event))
	}
	return result
}

func cloneOrderEvent(event orders.Event) orders.Event {
	raw, err := json.Marshal(event)
	if err != nil {
		return event
	}
	var cloned orders.Event
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return event
	}
	return cloned
}

func equivalentOrderEvent(left, right orders.Event) bool {
	left.TS = time.Time{}
	right.TS = time.Time{}
	return reflect.DeepEqual(left, right)
}

// compactOrderEvents removes complete histories of the oldest terminal
// orders. It never truncates an unresolved order or leaves a partial stream
// that could not be replayed after restart.
func compactOrderEvents(state *repositoryState, limit int) {
	if limit <= 0 || len(state.OrderEvents) <= limit {
		return
	}
	type group struct {
		clientID string
		last     time.Time
		count    int
		terminal bool
	}
	byClient := make(map[string]*group)
	for _, event := range state.OrderEvents {
		current := byClient[event.ClientID]
		if current == nil {
			current = &group{clientID: event.ClientID}
			byClient[event.ClientID] = current
		}
		current.count++
		current.last = event.TS
		current.terminal = orders.IsTerminal(event.Status)
	}
	candidates := make([]group, 0)
	for _, current := range byClient {
		if current.terminal {
			candidates = append(candidates, *current)
		}
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].last.Equal(candidates[right].last) {
			return candidates[left].clientID < candidates[right].clientID
		}
		return candidates[left].last.Before(candidates[right].last)
	})
	remove := make(map[string]struct{})
	remaining := len(state.OrderEvents)
	for _, candidate := range candidates {
		if remaining <= limit {
			break
		}
		remove[candidate.clientID] = struct{}{}
		remaining -= candidate.count
	}
	if len(remove) == 0 {
		return
	}
	compacted := make([]orders.Event, 0, remaining)
	for _, event := range state.OrderEvents {
		if _, discarded := remove[event.ClientID]; !discarded {
			compacted = append(compacted, event)
		}
	}
	state.OrderEvents = compacted
}
