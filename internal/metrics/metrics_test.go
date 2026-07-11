package metrics

import (
	"testing"
	"time"
)

func TestRunsSnapshot(t *testing.T) {
	m := NewRuns()
	slippage := 12.0
	m.Record("executed", &slippage)
	m.Record("not_approved", nil)
	m.Record("execution_failed", nil)
	m.RecordApprovalLatency(1500 * time.Millisecond)

	snapshot := m.Snapshot()
	if snapshot.Runs != 3 || snapshot.Executed != 1 || snapshot.NotApproved != 1 || snapshot.Errors != 1 {
		t.Fatalf("unexpected counters: %#v", snapshot)
	}
	if snapshot.ApprovalRate != 0.5 || snapshot.OrderSuccessRate != 0.5 {
		t.Fatalf("unexpected rates: %#v", snapshot)
	}
	if snapshot.SlippageHistogram["5-15"] != 1 || snapshot.ApprovalLatency.AverageSeconds != 1.5 {
		t.Fatalf("unexpected distributions: %#v", snapshot)
	}

	// The snapshot must not expose the mutable internal map.
	snapshot.SlippageHistogram["5-15"] = 99
	if m.Snapshot().SlippageHistogram["5-15"] != 1 {
		t.Fatal("Snapshot exposed internal histogram")
	}
}
