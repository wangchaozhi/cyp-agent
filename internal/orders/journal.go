package orders

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

// Event is one append-only journal entry. EventID makes replay idempotent:
// applying the same event twice is a no-op instead of a duplicate fill.
type Event struct {
	EventID  string                     `json:"event_id"`
	ClientID string                     `json:"client_id"`
	TS       time.Time                  `json:"ts"`
	Status   contracts.OrderStatus      `json:"status"`
	Intent   *contracts.OrderIntent     `json:"intent,omitempty"`
	Result   *contracts.ExecutionResult `json:"result,omitempty"`
	Note     string                     `json:"note,omitempty"`
}

// Order is the current materialized view of one order's journal.
type Order struct {
	ClientID string                     `json:"client_id"`
	Status   contracts.OrderStatus      `json:"status"`
	Intent   contracts.OrderIntent      `json:"intent"`
	Result   *contracts.ExecutionResult `json:"result,omitempty"`
	Events   []Event                    `json:"events"`
}

// EventStore is the durable append-only boundary used by Journal. Append must
// be idempotent by EventID and reject an EventID reused for different content.
type EventStore interface {
	LoadOrderEvents(context.Context) ([]Event, error)
	AppendOrderEvent(context.Context, Event) error
}

// Journal is a thread-safe order state machine backed by an optional durable
// append-only event store. Durable mutations are persisted before the
// materialized in-memory view changes, so a failed write can never authorize
// an order that cannot be recovered after restart.
type Journal struct {
	mu      sync.RWMutex
	orders  map[string]*Order
	applied map[string]Event
	store   EventStore
}

func NewJournal() *Journal {
	return &Journal{orders: make(map[string]*Order), applied: make(map[string]Event)}
}

// NewDurableJournal rebuilds the materialized order view from the complete
// persisted event stream before accepting new mutations.
func NewDurableJournal(ctx context.Context, store EventStore) (*Journal, error) {
	if ctx == nil {
		return nil, errors.New("order journal context is required")
	}
	if store == nil {
		return nil, errors.New("order event store is required")
	}
	events, err := store.LoadOrderEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("load order journal: %w", err)
	}
	journal, err := Replay(events)
	if err != nil {
		return nil, err
	}
	journal.store = store
	return journal, nil
}

// Open registers a new order in OrderStatusNew. It is idempotent for the
// same event ID so a crash between persist and ack can be replayed safely.
func (journal *Journal) Open(eventID string, intent contracts.OrderIntent) error {
	return journal.OpenContext(context.Background(), eventID, intent)
}

func (journal *Journal) OpenContext(ctx context.Context, eventID string, intent contracts.OrderIntent) error {
	if strings.TrimSpace(intent.ClientID) == "" {
		return errors.New("order intent requires a client id")
	}
	event := Event{
		EventID: eventID, ClientID: intent.ClientID, TS: time.Now().UTC(),
		Status: contracts.OrderStatusNew, Intent: &intent,
	}
	return journal.ApplyContext(ctx, event)
}

// Transition appends a status change for an existing order.
func (journal *Journal) Transition(
	eventID, clientID string,
	status contracts.OrderStatus,
	result *contracts.ExecutionResult,
	note string,
) error {
	return journal.TransitionContext(context.Background(), eventID, clientID, status, result, note)
}

func (journal *Journal) TransitionContext(
	ctx context.Context,
	eventID, clientID string,
	status contracts.OrderStatus,
	result *contracts.ExecutionResult,
	note string,
) error {
	event := Event{
		EventID: eventID, ClientID: clientID, TS: time.Now().UTC(),
		Status: status, Result: result, Note: note,
	}
	return journal.ApplyContext(ctx, event)
}

// Apply validates and applies one journal event. Replaying an event that was
// already applied returns nil without changing state, which makes crash
// recovery a plain re-read of the persisted event log.
func (journal *Journal) Apply(event Event) error {
	return journal.ApplyContext(context.Background(), event)
}

func (journal *Journal) ApplyContext(ctx context.Context, event Event) error {
	if ctx == nil {
		return errors.New("order journal context is required")
	}
	if strings.TrimSpace(event.EventID) == "" {
		return errors.New("order event requires an event id")
	}
	if strings.TrimSpace(event.ClientID) == "" {
		return errors.New("order event requires a client id")
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if existing, seen := journal.applied[event.EventID]; seen {
		if equivalentEvent(existing, event) {
			return nil
		}
		return fmt.Errorf("order event id %s was reused for different content", event.EventID)
	}
	if err := journal.validateLocked(event); err != nil {
		return err
	}
	if journal.store != nil {
		if err := journal.store.AppendOrderEvent(ctx, event); err != nil {
			return fmt.Errorf("persist order event %s: %w", event.EventID, err)
		}
	}
	journal.applyLocked(event)
	return nil
}

func (journal *Journal) validateLocked(event Event) error {
	current := journal.orders[event.ClientID]
	if current == nil {
		if event.Status != contracts.OrderStatusNew {
			return fmt.Errorf("order %s does not exist; first event must be %q, got %q",
				event.ClientID, contracts.OrderStatusNew, event.Status)
		}
		if event.Intent == nil {
			return fmt.Errorf("order %s open event requires the intent", event.ClientID)
		}
		return nil
	}
	if event.Status == contracts.OrderStatusNew {
		return fmt.Errorf("order %s already exists", event.ClientID)
	}
	return ValidateTransition(current.Status, event.Status)
}

func (journal *Journal) applyLocked(event Event) {
	current := journal.orders[event.ClientID]
	if current == nil {
		journal.orders[event.ClientID] = &Order{
			ClientID: event.ClientID, Status: contracts.OrderStatusNew,
			Intent: *event.Intent, Events: []Event{event},
		}
		journal.applied[event.EventID] = event
		return
	}
	current.Status = event.Status
	if event.Result != nil {
		result := *event.Result
		current.Result = &result
	}
	current.Events = append(current.Events, event)
	journal.applied[event.EventID] = event
}

// Get returns a copy of the order's materialized state.
func (journal *Journal) Get(clientID string) (Order, bool) {
	journal.mu.RLock()
	defer journal.mu.RUnlock()
	current := journal.orders[clientID]
	if current == nil {
		return Order{}, false
	}
	return copyOrder(current), true
}

// Unresolved lists orders that are neither terminal nor known-good, i.e.
// everything reconciliation must inspect after a restart. Unknown orders are
// always included because blind retries on them are forbidden.
func (journal *Journal) Unresolved() []Order {
	journal.mu.RLock()
	defer journal.mu.RUnlock()
	result := make([]Order, 0)
	for _, current := range journal.orders {
		if IsTerminal(current.Status) || current.Status == contracts.OrderStatusProtectivePlaced {
			continue
		}
		result = append(result, copyOrder(current))
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ClientID < result[right].ClientID
	})
	return result
}

// Orders returns all materialized orders, newest activity first. The returned
// values are detached copies suitable for audit APIs.
func (journal *Journal) Orders() []Order {
	journal.mu.RLock()
	defer journal.mu.RUnlock()
	result := make([]Order, 0, len(journal.orders))
	for _, current := range journal.orders {
		result = append(result, copyOrder(current))
	}
	sort.Slice(result, func(left, right int) bool {
		leftTS, rightTS := latestTS(result[left]), latestTS(result[right])
		if leftTS.Equal(rightTS) {
			return result[left].ClientID < result[right].ClientID
		}
		return leftTS.After(rightTS)
	})
	return result
}

// Replay rebuilds a journal from persisted events in order. It fails fast on
// the first inconsistent event so a corrupted log freezes startup instead of
// silently trading on top of wrong state.
func Replay(events []Event) (*Journal, error) {
	journal := NewJournal()
	for index, event := range events {
		if err := journal.Apply(event); err != nil {
			return nil, fmt.Errorf("replay event %d (%s): %w", index, event.EventID, err)
		}
	}
	return journal, nil
}

func copyOrder(current *Order) Order {
	result := Order{
		ClientID: current.ClientID, Status: current.Status, Intent: current.Intent,
		Events: append([]Event(nil), current.Events...),
	}
	if current.Result != nil {
		value := *current.Result
		result.Result = &value
	}
	return result
}

func equivalentEvent(left, right Event) bool {
	// Callers retrying the same deterministic event create a fresh timestamp;
	// identity is therefore the immutable business payload, not wall-clock time.
	left.TS = time.Time{}
	right.TS = time.Time{}
	return reflect.DeepEqual(left, right)
}

func latestTS(order Order) time.Time {
	if len(order.Events) == 0 {
		return time.Time{}
	}
	return order.Events[len(order.Events)-1].TS
}
