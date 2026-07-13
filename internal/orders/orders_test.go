package orders

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type journalStore struct {
	events []Event
	fail   bool
}

func (store *journalStore) LoadOrderEvents(context.Context) ([]Event, error) {
	return append([]Event(nil), store.events...), nil
}

func (store *journalStore) AppendOrderEvent(_ context.Context, event Event) error {
	if store.fail {
		return errors.New("disk unavailable")
	}
	store.events = append(store.events, event)
	return nil
}

func testIntent(clientID string) contracts.OrderIntent {
	return contracts.OrderIntent{
		ClientID: clientID, Symbol: "BTC/USDT", Venue: "paper",
		Side: contracts.SideLong, Instrument: contracts.InstrumentSpot,
		OrderType: contracts.EntryTypeMarket, SizeQuote: contracts.MustDecimal("100"),
	}
}

func TestTransitionTableCoversHappyPathAndTimeouts(t *testing.T) {
	happy := []contracts.OrderStatus{
		contracts.OrderStatusNew, contracts.OrderStatusPreflight,
		contracts.OrderStatusSubmitting, contracts.OrderStatusAcknowledged,
		contracts.OrderStatusPartiallyFilled, contracts.OrderStatusFilled,
		contracts.OrderStatusProtectivePlaced, contracts.OrderStatusFlattening,
		contracts.OrderStatusClosed,
	}
	for index := 1; index < len(happy); index++ {
		if err := ValidateTransition(happy[index-1], happy[index]); err != nil {
			t.Fatalf("happy path %s -> %s rejected: %v", happy[index-1], happy[index], err)
		}
	}
	// A submit timeout must resolve through unknown + reconciliation, never
	// by retrying the submit.
	if err := ValidateTransition(contracts.OrderStatusSubmitting, contracts.OrderStatusUnknown); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransition(contracts.OrderStatusUnknown, contracts.OrderStatusFilled); err != nil {
		t.Fatal(err)
	}
	if CanTransition(contracts.OrderStatusUnknown, contracts.OrderStatusSubmitting) {
		t.Fatal("unknown -> submitting would be a blind retry and must be illegal")
	}
}

func TestTerminalStatesAreFinal(t *testing.T) {
	for _, status := range []contracts.OrderStatus{
		contracts.OrderStatusClosed, contracts.OrderStatusCanceled,
		contracts.OrderStatusRejected, contracts.OrderStatusFailed,
	} {
		if !IsTerminal(status) {
			t.Fatalf("%s must be terminal", status)
		}
		if err := ValidateTransition(status, contracts.OrderStatusNew); err == nil {
			t.Fatalf("terminal %s accepted a transition", status)
		}
	}
	if IsTerminal(contracts.OrderStatusUnknown) {
		t.Fatal("unknown is not terminal; reconciliation must resolve it")
	}
}

func TestJournalLifecycleAndIdempotentReplay(t *testing.T) {
	journal := NewJournal()
	if err := journal.Open("evt-1", testIntent("order-1")); err != nil {
		t.Fatal(err)
	}
	// Replaying the same event is a no-op.
	if err := journal.Open("evt-1", testIntent("order-1")); err != nil {
		t.Fatalf("idempotent replay failed: %v", err)
	}
	if err := journal.Transition("evt-2", "order-1", contracts.OrderStatusSubmitting, nil, ""); err != nil {
		t.Fatal(err)
	}
	filled := &contracts.ExecutionResult{ClientID: "order-1", Status: contracts.OrderStatusFilled}
	if err := journal.Transition("evt-3", "order-1", contracts.OrderStatusFilled, filled, ""); err != nil {
		t.Fatal(err)
	}
	if err := journal.Transition("evt-3", "order-1", contracts.OrderStatusFilled, filled, ""); err != nil {
		t.Fatalf("duplicate fill event must be ignored: %v", err)
	}
	order, ok := journal.Get("order-1")
	if !ok || order.Status != contracts.OrderStatusFilled || len(order.Events) != 3 {
		t.Fatalf("order state = %+v (ok=%v)", order, ok)
	}
	if order.Result == nil || order.Result.Status != contracts.OrderStatusFilled {
		t.Fatalf("materialized result = %+v", order.Result)
	}
}

func TestJournalRejectsIllegalPaths(t *testing.T) {
	journal := NewJournal()
	if err := journal.Transition("evt-x", "ghost", contracts.OrderStatusFilled, nil, ""); err == nil {
		t.Fatal("transition on a missing order must fail")
	}
	if err := journal.Open("evt-1", testIntent("order-1")); err != nil {
		t.Fatal(err)
	}
	if err := journal.Open("evt-2", testIntent("order-1")); err == nil {
		t.Fatal("duplicate open with a fresh event id must fail")
	}
	if err := journal.Transition("evt-3", "order-1", contracts.OrderStatusClosed, nil, ""); err == nil {
		t.Fatal("new -> closed skips execution and must be illegal")
	}
	if err := journal.Open("evt-4", contracts.OrderIntent{}); err == nil {
		t.Fatal("empty client id must fail")
	}
}

func TestReplayRebuildsStateAndFailsFastOnCorruption(t *testing.T) {
	journal := NewJournal()
	if err := journal.Open("evt-1", testIntent("order-1")); err != nil {
		t.Fatal(err)
	}
	if err := journal.Transition("evt-2", "order-1", contracts.OrderStatusSubmitting, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := journal.Transition("evt-3", "order-1", contracts.OrderStatusUnknown, nil, "submit timeout"); err != nil {
		t.Fatal(err)
	}
	source, _ := journal.Get("order-1")

	rebuilt, err := Replay(source.Events)
	if err != nil {
		t.Fatal(err)
	}
	order, ok := rebuilt.Get("order-1")
	if !ok || order.Status != contracts.OrderStatusUnknown {
		t.Fatalf("replayed status = %+v (ok=%v)", order, ok)
	}
	unresolved := rebuilt.Unresolved()
	if len(unresolved) != 1 || unresolved[0].ClientID != "order-1" {
		t.Fatalf("unresolved = %+v", unresolved)
	}

	corrupted := append([]Event(nil), source.Events...)
	corrupted[1], corrupted[2] = corrupted[2], corrupted[1]
	if _, err := Replay(corrupted); err == nil || !strings.Contains(err.Error(), "replay event") {
		t.Fatalf("corrupted log error = %v", err)
	}
}

func TestUnresolvedSkipsTerminalOrders(t *testing.T) {
	journal := NewJournal()
	if err := journal.Open("evt-1", testIntent("order-done")); err != nil {
		t.Fatal(err)
	}
	if err := journal.Transition("evt-2", "order-done", contracts.OrderStatusRejected, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := journal.Open("evt-3", testIntent("order-open")); err != nil {
		t.Fatal(err)
	}
	unresolved := journal.Unresolved()
	if len(unresolved) != 1 || unresolved[0].ClientID != "order-open" {
		t.Fatalf("unresolved = %+v", unresolved)
	}
}

func TestDurableJournalPersistsBeforeMutationAndRestores(t *testing.T) {
	store := &journalStore{fail: true}
	journal, err := NewDurableJournal(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.OpenContext(context.Background(), "evt-durable", testIntent("durable-order")); err == nil {
		t.Fatal("failed durable append unexpectedly opened the order")
	}
	if _, exists := journal.Get("durable-order"); exists {
		t.Fatal("memory changed before durable append succeeded")
	}
	store.fail = false
	if err := journal.OpenContext(context.Background(), "evt-durable", testIntent("durable-order")); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 1 {
		t.Fatalf("persisted events = %d, want 1", len(store.events))
	}
	restored, err := NewDurableJournal(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	order, exists := restored.Get("durable-order")
	if !exists || order.Status != contracts.OrderStatusNew {
		t.Fatalf("restored order = %+v, exists=%v", order, exists)
	}
	if err := restored.TransitionContext(context.Background(), "evt-durable", "durable-order", contracts.OrderStatusCanceled, nil, "collision"); err == nil {
		t.Fatal("event id collision with different content was accepted")
	}
}
