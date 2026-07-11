package observability

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestTraceConcurrentSpansAndContext(t *testing.T) {
	t.Parallel()
	trace := NewTrace("run-1")
	ctx := ContextWithTrace(context.Background(), trace)
	if found, ok := TraceFromContext(ctx); !ok || found != trace {
		t.Fatal("trace missing from context")
	}

	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			span := trace.StartSpan("worker")
			if index%2 == 0 {
				span.End(errors.New("failed"))
			} else {
				span.End(nil)
			}
			span.End(errors.New("second end must be ignored"))
		}()
	}
	wait.Wait()
	summary := trace.Summary()
	if summary.TraceID != "run-1" || len(summary.Spans) != 20 {
		t.Fatalf("summary = %#v", summary)
	}
	errorsSeen := 0
	for _, span := range summary.Spans {
		if span.MS < 0 || span.Finished == "" {
			t.Fatalf("invalid span: %#v", span)
		}
		if span.Status == "error" {
			errorsSeen++
			if span.Error != "failed" {
				t.Fatalf("span was ended twice: %#v", span)
			}
		}
	}
	if errorsSeen != 10 {
		t.Fatalf("error spans = %d, want 10", errorsSeen)
	}
}

func TestRuntimeMetricsConcurrent(t *testing.T) {
	t.Parallel()
	metrics := &RuntimeMetrics{}
	var wait sync.WaitGroup
	for index := 0; index < 100; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			var err error
			if index%4 == 0 {
				err = errors.New("failure")
			}
			metrics.RecordScan(err)
			metrics.RecordMonitor(nil)
			metrics.RecordReconcile(err)
			metrics.RecordAlert()
		}()
	}
	wait.Wait()
	snapshot := metrics.Snapshot()
	if snapshot.ScanCycles != 100 || snapshot.ScanErrors != 25 || snapshot.MonitorCycles != 100 ||
		snapshot.ReconcileAttempts != 100 || snapshot.ReconcileFailures != 25 || snapshot.Alerts != 100 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
