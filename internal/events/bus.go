// Package events provides the in-process event fan-out used by the API's SSE
// endpoint. Delivery is deliberately best effort: a slow subscriber must never
// stall trading or risk-control work.
package events

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// Event is the wire-compatible dashboard event envelope. Data is flattened
// into the top-level JSON object so events remain extensible without changing
// this package for every new payload field.
type Event struct {
	Type  string
	RunID string
	TS    time.Time
	Data  map[string]any
}

// NewEvent builds an event with an RFC3339 UTC timestamp. Publish also fills a
// missing timestamp, so callers may construct Event values directly.
func NewEvent(eventType, runID string, data map[string]any) Event {
	return Event{
		Type:  eventType,
		RunID: runID,
		TS:    time.Now().UTC(),
		Data:  cloneMap(data),
	}
}

// MarshalJSON preserves the existing flat SSE payload shape:
// {"type":"...","run_id":"...","ts":"...", ...data}.
func (e Event) MarshalJSON() ([]byte, error) {
	payload := cloneMap(e.Data)
	if payload == nil {
		payload = make(map[string]any, 3)
	}
	payload["type"] = e.Type
	payload["run_id"] = e.RunID
	payload["ts"] = e.TS.UTC().Format(time.RFC3339Nano)
	return json.Marshal(payload)
}

// UnmarshalJSON accepts the same flat event shape and retains unknown fields
// in Data.
func (e *Event) UnmarshalJSON(raw []byte) error {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if value, ok := payload["type"]; ok {
		if err := json.Unmarshal(value, &e.Type); err != nil {
			return err
		}
		delete(payload, "type")
	}
	if value, ok := payload["run_id"]; ok {
		if err := json.Unmarshal(value, &e.RunID); err != nil {
			return err
		}
		delete(payload, "run_id")
	}
	if value, ok := payload["ts"]; ok {
		var stamp string
		if err := json.Unmarshal(value, &stamp); err != nil {
			return err
		}
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return err
		}
		e.TS = parsed.UTC()
		delete(payload, "ts")
	}
	e.Data = make(map[string]any, len(payload))
	for key, value := range payload {
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			return err
		}
		e.Data[key] = decoded
	}
	return nil
}

// Subscription owns one event channel. Cancel is idempotent and closes C.
type Subscription struct {
	C       <-chan Event
	bus     *Bus
	id      uint64
	dropped atomic.Uint64
	once    sync.Once
}

// Cancel removes the subscriber and closes its event channel.
func (s *Subscription) Cancel() {
	if s == nil || s.bus == nil {
		return
	}
	s.once.Do(func() { s.bus.unsubscribe(s.id) })
}

// Dropped reports how many events this subscriber missed because its buffer
// was full. It is primarily an observability signal for SSE handlers.
func (s *Subscription) Dropped() uint64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

type subscriber struct {
	ch  chan Event
	sub *Subscription
}

// Bus is a concurrency-safe, non-blocking in-process event bus. historyLimit
// controls the optional bounded replay buffer; zero disables history.
type Bus struct {
	mu           sync.RWMutex
	subscribers  map[uint64]*subscriber
	nextID       uint64
	history      []Event
	historyLimit int
	closed       bool
	now          func() time.Time
}

// EventBus is the domain-oriented name retained for the public
// implementation. Bus remains the shorter concrete type used internally.
type EventBus = Bus

// NewBus creates a bus. A negative historyLimit is treated as zero.
func NewBus(historyLimit int) *Bus {
	if historyLimit < 0 {
		historyLimit = 0
	}
	return &Bus{
		subscribers:  make(map[uint64]*subscriber),
		historyLimit: historyLimit,
		now:          time.Now,
	}
}

// NewEventBus creates an EventBus; omitting historyLimit disables replay.
func NewEventBus(historyLimit ...int) *EventBus {
	limit := 0
	if len(historyLimit) > 0 {
		limit = historyLimit[0]
	}
	return NewBus(limit)
}

// Subscribe registers a subscriber with the requested channel capacity.
// Delivery is non-blocking even when buffer is zero.
func (b *Bus) Subscribe(buffer int) *Subscription {
	return b.SubscribeReplay(buffer, 0, time.Time{})
}

// SubscribeReplay atomically registers a subscriber and preloads its channel
// with the newest retained events after the supplied timestamp. Registration
// and replay happen under one lock, so events cannot disappear in the gap
// between reading history and subscribing to live delivery.
func (b *Bus) SubscribeReplay(buffer, limit int, after time.Time) *Subscription {
	if buffer < 0 {
		buffer = 0
	}
	if limit < 0 {
		limit = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	replay := b.replayLocked(limit, after)
	// Keep the requested live-delivery headroom in addition to replayed
	// frames; otherwise a full replay would make the first live event drop.
	ch := make(chan Event, buffer+len(replay))
	if b.closed {
		close(ch)
		return &Subscription{C: ch}
	}
	b.nextID++
	sub := &Subscription{C: ch, bus: b, id: b.nextID}
	b.subscribers[sub.id] = &subscriber{ch: ch, sub: sub}
	for _, event := range replay {
		ch <- event
	}
	return sub
}

func (b *Bus) replayLocked(limit int, after time.Time) []Event {
	if limit == 0 || len(b.history) == 0 {
		return nil
	}
	matched := make([]Event, 0, min(limit, len(b.history)))
	for _, event := range b.history {
		if !after.IsZero() && !event.TS.After(after) {
			continue
		}
		matched = append(matched, cloneEvent(event))
	}
	if len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	return matched
}

// Emit creates and publishes an event, returning the timestamped value.
func (b *Bus) Emit(eventType, runID string, data map[string]any) Event {
	event := Event{Type: eventType, RunID: runID, TS: b.now().UTC(), Data: cloneMap(data)}
	b.Publish(event)
	return event
}

// Publish broadcasts an event without waiting for consumers. It returns false
// after the bus has been closed. A full subscriber buffer drops only that
// subscriber's copy.
func (b *Bus) Publish(event Event) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	if event.TS.IsZero() {
		event.TS = b.now().UTC()
	} else {
		event.TS = event.TS.UTC()
	}
	event.Data = cloneMap(event.Data)
	b.appendHistory(event)
	for _, target := range b.subscribers {
		select {
		case target.ch <- cloneEvent(event):
		default:
			target.sub.dropped.Add(1)
		}
	}
	return true
}

// History returns events in publication order. Empty runID selects all runs;
// positive limit returns only the most recent matching events. Returned events
// are independent copies.
func (b *Bus) History(runID string, limit int) []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	matched := make([]Event, 0, len(b.history))
	for _, event := range b.history {
		if runID == "" || event.RunID == runID {
			matched = append(matched, cloneEvent(event))
		}
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	return matched
}

// SubscriberCount returns the number of live subscribers.
func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// Close closes every subscription and rejects future publications. It is
// idempotent.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, target := range b.subscribers {
		delete(b.subscribers, id)
		close(target.ch)
	}
}

func (b *Bus) unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	target, ok := b.subscribers[id]
	if !ok {
		return
	}
	delete(b.subscribers, id)
	close(target.ch)
}

func (b *Bus) appendHistory(event Event) {
	if b.historyLimit == 0 {
		return
	}
	if len(b.history) == b.historyLimit {
		copy(b.history, b.history[1:])
		b.history[len(b.history)-1] = cloneEvent(event)
		return
	}
	b.history = append(b.history, cloneEvent(event))
}

func cloneEvent(event Event) Event {
	event.Data = cloneMap(event.Data)
	return event
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = cloneValue(value)
	}
	return output
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for i := range typed {
			result[i] = cloneValue(typed[i])
		}
		return result
	case []map[string]any:
		result := make([]map[string]any, len(typed))
		for i := range typed {
			result[i] = cloneMap(typed[i])
		}
		return result
	default:
		return value
	}
}
