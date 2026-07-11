package events

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestEventJSONUsesFlatEnvelope(t *testing.T) {
	event := Event{
		Type:  "run_done",
		RunID: "abc123",
		TS:    time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC),
		Data:  map[string]any{"symbol": "BTC/USDT", "status": "executed"},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["type"] != "run_done" || payload["run_id"] != "abc123" {
		t.Fatalf("unexpected envelope: %s", raw)
	}
	if payload["symbol"] != "BTC/USDT" || payload["status"] != "executed" {
		t.Fatalf("event data was not flattened: %s", raw)
	}
	if _, nested := payload["data"]; nested {
		t.Fatalf("wire event must not contain a nested data object: %s", raw)
	}

	var decoded Event
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Type != event.Type || decoded.RunID != event.RunID || decoded.Data["symbol"] != "BTC/USDT" {
		t.Fatalf("round trip mismatch: %#v", decoded)
	}
}

func TestBusSubscribeCancelAndClose(t *testing.T) {
	bus := NewBus(0)
	sub := bus.Subscribe(1)
	if got := bus.SubscriberCount(); got != 1 {
		t.Fatalf("SubscriberCount()=%d, want 1", got)
	}
	if ok := bus.Publish(NewEvent("run_started", "r1", map[string]any{"symbol": "BTC/USDT"})); !ok {
		t.Fatal("Publish returned false on an open bus")
	}
	select {
	case event := <-sub.C:
		if event.Type != "run_started" || event.RunID != "r1" {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	sub.Cancel()
	sub.Cancel()
	if _, open := <-sub.C; open {
		t.Fatal("subscription channel remained open after Cancel")
	}
	if got := bus.SubscriberCount(); got != 0 {
		t.Fatalf("SubscriberCount()=%d, want 0", got)
	}

	bus.Close()
	bus.Close()
	if bus.Publish(NewEvent("ignored", "r1", nil)) {
		t.Fatal("Publish succeeded after Close")
	}
	closed := bus.Subscribe(1)
	if _, open := <-closed.C; open {
		t.Fatal("subscription created after Close must be closed")
	}
}

func TestSlowSubscriberNeverBlocksPublisher(t *testing.T) {
	bus := NewBus(0)
	sub := bus.Subscribe(1)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10_000; i++ {
			bus.Emit("tick", "-", map[string]any{"n": i})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher blocked on a slow subscriber")
	}
	if sub.Dropped() == 0 {
		t.Fatal("expected overflow to be counted")
	}
	sub.Cancel()
}

func TestHistoryIsBoundedFilteredAndCopied(t *testing.T) {
	bus := NewBus(3)
	for i, runID := range []string{"r1", "r2", "r1", "r1"} {
		bus.Emit("step", runID, map[string]any{"n": i, "nested": map[string]any{"safe": true}})
	}
	all := bus.History("", 0)
	if len(all) != 3 || all[0].Data["n"] != 1 || all[2].Data["n"] != 3 {
		t.Fatalf("unexpected bounded history: %#v", all)
	}
	r1 := bus.History("r1", 1)
	if len(r1) != 1 || r1[0].Data["n"] != 3 {
		t.Fatalf("unexpected filtered history: %#v", r1)
	}
	r1[0].Data["n"] = 99
	r1[0].Data["nested"].(map[string]any)["safe"] = false
	again := bus.History("r1", 1)
	if again[0].Data["n"] != 3 || again[0].Data["nested"].(map[string]any)["safe"] != true {
		t.Fatal("History leaked mutable internal state")
	}
}

func TestConcurrentPublishAndSubscription(t *testing.T) {
	bus := NewBus(32)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				sub := bus.Subscribe(4)
				bus.Emit("work", fmt.Sprintf("r%d", worker), map[string]any{"i": i})
				sub.Cancel()
			}
		}(worker)
	}
	wg.Wait()
	if got := bus.SubscriberCount(); got != 0 {
		t.Fatalf("leaked %d subscribers", got)
	}
	if got := len(bus.History("", 0)); got != 32 {
		t.Fatalf("history size=%d, want 32", got)
	}
}
