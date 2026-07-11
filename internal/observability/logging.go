// Package observability provides structured JSON logging, redaction, traces,
// and runtime metrics without third-party dependencies.
package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
)

const Mask = "***"

var sensitiveHints = []string{
	"api_key", "apikey", "api_secret", "secret", "private_key", "mnemonic",
	"password", "token", "authorization", "db_url", "database_url", "dsn",
}

func IsSensitiveKey(key string) bool {
	key = strings.ToLower(key)
	for _, hint := range sensitiveHints {
		if strings.Contains(key, hint) {
			return true
		}
	}
	return false
}

// Redact returns a JSON-safe deep copy with sensitive-key values replaced by
// Mask. It never mutates caller-owned maps or slices.
func Redact(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case error:
		return typed.Error()
	case map[string]any:
		return redactMap(typed)
	case map[string]string:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if IsSensitiveKey(key) {
				result[key] = Mask
			} else {
				result[key] = item
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = Redact(item)
		}
		return result
	case []map[string]any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = redactMap(item)
		}
		return result
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return value
	}
	return redactDecoded(decoded)
}

func redactMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		if IsSensitiveKey(key) {
			result[key] = Mask
			continue
		}
		result[key] = Redact(value)
	}
	return result
}

func redactDecoded(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if IsSensitiveKey(key) {
				result[key] = Mask
			} else {
				result[key] = redactDecoded(item)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = redactDecoded(item)
		}
		return result
	default:
		return value
	}
}

// RedactingHandler wraps any slog handler and sanitizes both per-record and
// With attributes before the underlying handler sees them.
type RedactingHandler struct {
	next slog.Handler
}

func NewRedactingHandler(next slog.Handler) *RedactingHandler {
	if next == nil {
		panic("observability: nil slog handler")
	}
	return &RedactingHandler{next: next}
}

func (handler *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler *RedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		clean.AddAttrs(redactAttr(attribute))
		return true
	})
	return handler.next.Handle(ctx, clean)
}

func (handler *RedactingHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attributes))
	for index, attribute := range attributes {
		clean[index] = redactAttr(attribute)
	}
	return &RedactingHandler{next: handler.next.WithAttrs(clean)}
}

func (handler *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{next: handler.next.WithGroup(name)}
}

func redactAttr(attribute slog.Attr) slog.Attr {
	attribute.Value = attribute.Value.Resolve()
	if IsSensitiveKey(attribute.Key) {
		return slog.String(attribute.Key, Mask)
	}
	if attribute.Value.Kind() == slog.KindGroup {
		group := attribute.Value.Group()
		clean := make([]slog.Attr, len(group))
		for index, nested := range group {
			clean[index] = redactAttr(nested)
		}
		return slog.Group(attribute.Key, attrsToAny(clean)...)
	}
	if attribute.Value.Kind() == slog.KindAny {
		attribute.Value = slog.AnyValue(Redact(attribute.Value.Any()))
	}
	return attribute
}

func attrsToAny(attributes []slog.Attr) []any {
	result := make([]any, len(attributes))
	for index := range attributes {
		result[index] = attributes[index]
	}
	return result
}

// NewJSONLogger emits one JSON object per line. Standard slog keys are renamed
// for API stability: ts, level, msg, plus a stable logger field.
func NewJSONLogger(output io.Writer, level slog.Leveler, name string) *slog.Logger {
	if output == nil {
		output = io.Discard
	}
	if level == nil {
		level = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
			if attribute.Key == slog.TimeKey {
				attribute.Key = "ts"
			}
			return attribute
		},
	})
	logger := slog.New(NewRedactingHandler(handler))
	if strings.TrimSpace(name) != "" {
		logger = logger.With("logger", "cyp."+strings.TrimSpace(name))
	}
	return logger
}

func DefaultLogger(name string) *slog.Logger {
	return NewJSONLogger(os.Stderr, slog.LevelInfo, name)
}

// JoinErrors removes nils and preserves every sink/worker failure.
func JoinErrors(values ...error) error {
	filtered := make([]error, 0, len(values))
	for _, value := range values {
		if value != nil {
			filtered = append(filtered, value)
		}
	}
	return errors.Join(filtered...)
}
