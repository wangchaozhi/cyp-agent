package observability

import (
	"context"
	"sync"
	"time"
)

type SpanSummary struct {
	Name     string  `json:"name"`
	MS       float64 `json:"ms"`
	Status   string  `json:"status"`
	Error    string  `json:"error,omitempty"`
	Started  string  `json:"started_at,omitempty"`
	Finished string  `json:"finished_at,omitempty"`
}

type TraceSummary struct {
	TraceID string        `json:"trace_id"`
	Spans   []SpanSummary `json:"spans"`
}

type spanState struct {
	name     string
	started  time.Time
	finished time.Time
	status   string
	error    string
}

// Trace is safe for spans completed by concurrent analyst goroutines.
type Trace struct {
	mu    sync.RWMutex
	id    string
	now   func() time.Time
	spans []*spanState
}

func NewTrace(traceID string) *Trace {
	return &Trace{id: traceID, now: time.Now, spans: make([]*spanState, 0)}
}

func (trace *Trace) ID() string {
	if trace == nil {
		return ""
	}
	return trace.id
}

type Span struct {
	trace *Trace
	state *spanState
	once  sync.Once
}

func (trace *Trace) StartSpan(name string) *Span {
	if trace == nil {
		return &Span{}
	}
	state := &spanState{name: name, started: trace.now().UTC(), status: "ok"}
	trace.mu.Lock()
	trace.spans = append(trace.spans, state)
	trace.mu.Unlock()
	return &Span{trace: trace, state: state}
}

// End is idempotent. A non-nil error marks the span as failed while preserving
// the original error string for diagnostics; summaries never contain secrets
// unless callers put them directly into an error message.
func (span *Span) End(err error) {
	if span == nil || span.trace == nil || span.state == nil {
		return
	}
	span.once.Do(func() {
		span.trace.mu.Lock()
		defer span.trace.mu.Unlock()
		span.state.finished = span.trace.now().UTC()
		if err != nil {
			span.state.status = "error"
			span.state.error = err.Error()
		}
	})
}

func (trace *Trace) Run(ctx context.Context, name string, operation func(context.Context) error) error {
	span := trace.StartSpan(name)
	err := operation(ctx)
	span.End(err)
	return err
}

func (trace *Trace) Summary() TraceSummary {
	if trace == nil {
		return TraceSummary{Spans: []SpanSummary{}}
	}
	trace.mu.RLock()
	defer trace.mu.RUnlock()
	now := trace.now().UTC()
	result := TraceSummary{TraceID: trace.id, Spans: make([]SpanSummary, 0, len(trace.spans))}
	for _, span := range trace.spans {
		finished := span.finished
		if finished.IsZero() {
			finished = now
		}
		duration := float64(finished.Sub(span.started).Microseconds()) / 1000
		if duration < 0 {
			duration = 0
		}
		summary := SpanSummary{
			Name: span.name, MS: duration, Status: span.status, Error: span.error,
			Started: span.started.Format(time.RFC3339Nano),
		}
		if !span.finished.IsZero() {
			summary.Finished = span.finished.Format(time.RFC3339Nano)
		}
		result.Spans = append(result.Spans, summary)
	}
	return result
}

type traceContextKey struct{}

func ContextWithTrace(ctx context.Context, trace *Trace) context.Context {
	return context.WithValue(ctx, traceContextKey{}, trace)
}

func TraceFromContext(ctx context.Context) (*Trace, bool) {
	if ctx == nil {
		return nil, false
	}
	trace, ok := ctx.Value(traceContextKey{}).(*Trace)
	return trace, ok && trace != nil
}
