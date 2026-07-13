package orders

import (
	"errors"
	"fmt"
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
	ClientID string
	Status   contracts.OrderStatus
	Intent   contracts.OrderIntent
	Result   *contracts.ExecutionResult
	Events   []Event
}

// Journal is an in-memory, thread-safe order state machine with an
// append-only event log. A durable implementation replays the same Apply
// calls from the orders/order_events tables on startup.
type Journal struct {
	mu      sync.RWMutex
	orders  map[string]*Order
	applied map[string]struct{}
}

func NewJournal() *Journal {
	return &Journal{orders: make(map[string]*Order), applied: make(map[string]struct{})}
}

// Open registers a new order in OrderStatusNew. It is idempotent for the
// same event ID so a crash between persist and ack can be replayed safely.
func (journal *Journal) Open(eventID string, intent contracts.OrderIntent) error {
	if strings.TrimSpace(intent.ClientID) == "" {
		return errors.New("order intent requires a client id")
	}
	event := Event{
		EventID: eventID, ClientID: intent.ClientID, TS: time.Now().UTC(),
		Status: contracts.OrderStatusNew, Intent: &intent,
	}
	return journal.Apply(event)
}

// Transition appends a status change for an existing order.
func (journal *Journal) Transition(
	eventID, clientID string,
	status contracts.OrderStatus,
	result *contracts.ExecutionResult,
	note string,
) error {
	event := Event{
		EventID: eventID, ClientID: clientID, TS: time.Now().UTC(),
		Status: status, Result: result, Note: note,
	}
	return journal.Apply(event)
}

// Apply validates and applies one journal event. Replaying an event that was
// already applied returns nil without changing state, which makes crash
// recovery a plain re-read of the persisted event log.
func (journal *Journal) Apply(event Event) error {
	if strings.TrimSpace(event.EventID) == "" {
		return errors.New("order event requires an event id")
	}
	if strings.TrimSpace(event.ClientID) == "" {
		return errors.New("order event requires a client id")
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if _, seen := journal.applied[event.EventID]; seen {
		return nil
	}
	current := journal.orders[event.ClientID]
	if current == nil {
		if event.Status != contracts.OrderStatusNew {
			return fmt.Errorf("order %s does not exist; first event must be %q, got %q",
				event.ClientID, contracts.OrderStatusNew, event.Status)
		}
		if event.Intent == nil {
			return fmt.Errorf("order %s open event requires the intent", event.ClientID)
		}
		journal.orders[event.ClientID] = &Order{
			ClientID: event.ClientID, Status: contracts.OrderStatusNew,
			Intent: *event.Intent, Events: []Event{event},
		}
		journal.applied[event.EventID] = struct{}{}
		return nil
	}
	if event.Status == contracts.OrderStatusNew {
		return fmt.Errorf("order %s already exists", event.ClientID)
	}
	if err := ValidateTransition(current.Status, event.Status); err != nil {
		return err
	}
	current.Status = event.Status
	if event.Result != nil {
		result := *event.Result
		current.Result = &result
	}
	current.Events = append(current.Events, event)
	journal.applied[event.EventID] = struct{}{}
	return nil
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
		if IsTerminal(current.Status) {
			continue
		}
		result = append(result, copyOrder(current))
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ClientID < result[right].ClientID
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
