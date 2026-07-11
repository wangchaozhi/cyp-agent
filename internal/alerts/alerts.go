// Package alerts dispatches redacted alerts to isolated sinks.
package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/observability"
)

type Alert struct {
	Level  string
	Msg    string
	TS     time.Time
	Fields map[string]any
}

// MarshalJSON preserves the public webhook shape: level/msg/ts and custom
// fields share one object. Reserved fields always win over caller data.
func (alert Alert) MarshalJSON() ([]byte, error) {
	payload := make(map[string]any, len(alert.Fields)+3)
	for key, value := range alert.Fields {
		if key == "level" || key == "msg" || key == "ts" {
			continue
		}
		payload[key] = observability.Redact(value)
	}
	payload["level"] = alert.Level
	payload["msg"] = alert.Msg
	payload["ts"] = alert.TS.UTC().Format(time.RFC3339Nano)
	return json.Marshal(payload)
}

type Sink interface {
	Emit(ctx context.Context, alert Alert) error
}

type Alerter struct {
	sinks   []Sink
	logger  *slog.Logger
	metrics *observability.RuntimeMetrics
	now     func() time.Time
}

func NewAlerter(logger *slog.Logger, metrics *observability.RuntimeMetrics, sinks ...Sink) *Alerter {
	if logger == nil {
		logger = observability.DefaultLogger("alert")
	}
	cleanSinks := make([]Sink, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			cleanSinks = append(cleanSinks, sink)
		}
	}
	return &Alerter{sinks: cleanSinks, logger: logger, metrics: metrics, now: time.Now}
}

// Alert attempts every sink even when an earlier sink fails. The joined error
// is diagnostic only; trading/runtime callers should not let a notification
// outage bypass or reverse a safety decision.
func (alerter *Alerter) Alert(
	ctx context.Context,
	level, message string,
	fields map[string]any,
) error {
	if alerter == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("alert context is required")
	}
	level = strings.TrimSpace(level)
	if level == "" {
		level = "warning"
	}
	alert := Alert{
		Level: level, Msg: strings.TrimSpace(message), TS: alerter.now().UTC(),
		Fields: redactedFields(fields),
	}
	alerter.metrics.RecordAlert()
	errorsSeen := make([]error, 0)
	for _, sink := range alerter.sinks {
		if err := sink.Emit(ctx, alert); err != nil {
			errorsSeen = append(errorsSeen, err)
			alerter.logger.ErrorContext(ctx, "alert_sink_failed", "error", err.Error())
		}
	}
	return errors.Join(errorsSeen...)
}

func redactedFields(fields map[string]any) map[string]any {
	if fields == nil {
		return map[string]any{}
	}
	redacted, ok := observability.Redact(fields).(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return redacted
}

type ConsoleSink struct {
	Logger *slog.Logger
}

func (sink ConsoleSink) Emit(ctx context.Context, alert Alert) error {
	logger := sink.Logger
	if logger == nil {
		logger = observability.DefaultLogger("alert")
	}
	arguments := make([]any, 0, len(alert.Fields)*2+4)
	arguments = append(arguments, "alert_level", alert.Level, "alert_ts", alert.TS.UTC())
	keys := make([]string, 0, len(alert.Fields))
	for key := range alert.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		arguments = append(arguments, key, alert.Fields[key])
	}
	logger.WarnContext(ctx, alert.Msg, arguments...)
	return nil
}

// Build creates the default console sink and an optional webhook sink.
func Build(
	logger *slog.Logger,
	metrics *observability.RuntimeMetrics,
	webhookURL string,
) (*Alerter, error) {
	sinks := []Sink{ConsoleSink{Logger: logger}}
	if strings.TrimSpace(webhookURL) != "" {
		webhook, err := NewWebhookSink(webhookURL, nil)
		if err != nil {
			return nil, err
		}
		sinks = append(sinks, webhook)
	}
	return NewAlerter(logger, metrics, sinks...), nil
}
