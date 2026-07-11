package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/observability"
)

func TestWebhookSinkPostsRedactedFlatJSON(t *testing.T) {
	t.Parallel()
	type receivedRequest struct {
		contentType string
		payload     map[string]any
	}
	received := make(chan receivedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode body: %v", err)
		}
		received <- receivedRequest{contentType: request.Header.Get("Content-Type"), payload: payload}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sink, err := NewWebhookSink(server.URL+"?token=must-never-be-logged", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	metrics := &observability.RuntimeMetrics{}
	alerter := NewAlerter(observability.NewJSONLogger(&bytes.Buffer{}, slog.LevelDebug, "test"), metrics, sink)
	if err := alerter.Alert(context.Background(), "warning", "risk_circuit", map[string]any{
		"symbol": "BTC/USDT", "api_secret": "actual-secret",
		"nested": map[string]any{"authorization": "Bearer secret"},
	}); err != nil {
		t.Fatal(err)
	}
	request := <-received
	if request.contentType != "application/json" {
		t.Fatalf("content type = %q", request.contentType)
	}
	if request.payload["level"] != "warning" || request.payload["msg"] != "risk_circuit" || request.payload["symbol"] != "BTC/USDT" {
		t.Fatalf("payload = %#v", request.payload)
	}
	if request.payload["api_secret"] != observability.Mask || request.payload["ts"] == nil {
		t.Fatalf("redaction/timestamp missing: %#v", request.payload)
	}
	if metrics.Snapshot().Alerts != 1 {
		t.Fatal("alert metric not recorded")
	}
}

type testSink struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (sink *testSink) Emit(_ context.Context, _ Alert) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.calls++
	return sink.err
}

func (sink *testSink) Calls() int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.calls
}

func TestAlerterIsolatesSinkFailures(t *testing.T) {
	t.Parallel()
	first := &testSink{err: errors.New("first failed")}
	second := &testSink{}
	var logs bytes.Buffer
	alerter := NewAlerter(observability.NewJSONLogger(&logs, slog.LevelDebug, "alert"), nil, first, second)
	err := alerter.Alert(context.Background(), "error", "execution_failed", nil)
	if err == nil || first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("error=%v first=%d second=%d", err, first.Calls(), second.Calls())
	}
	if !strings.Contains(logs.String(), "alert_sink_failed") {
		t.Fatalf("sink failure not logged: %s", logs.String())
	}
}

func TestWebhookRejectsStatusAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/slow" {
			<-request.Context().Done()
			return
		}
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	failed, err := NewWebhookSink(server.URL+"/failed", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	alert := Alert{Level: "warning", Msg: "test", TS: time.Now(), Fields: map[string]any{}}
	if err := failed.Emit(context.Background(), alert); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("status error = %v", err)
	}

	slow, err := NewWebhookSink(server.URL+"/slow?token=secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := slow.Emit(ctx, alert); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}
