package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactAndJSONLogger(t *testing.T) {
	t.Parallel()
	input := map[string]any{
		"safe":    "visible",
		"api_key": "top-secret",
		"nested": []any{map[string]any{
			"authorization": "Bearer secret", "value": 7,
		}},
	}
	redacted := Redact(input).(map[string]any)
	if redacted["api_key"] != Mask {
		t.Fatalf("top-level secret not masked: %#v", redacted)
	}
	if input["api_key"] != "top-secret" {
		t.Fatal("redaction mutated caller input")
	}

	var output bytes.Buffer
	logger := NewJSONLogger(&output, slog.LevelDebug, "test")
	logger.With("password", "hidden").InfoContext(context.Background(), "message",
		"payload", input,
		slog.Group("credentials", "private_key", "key", "safe", "ok"),
	)
	line := strings.TrimSpace(output.String())
	if strings.Contains(line, "top-secret") || strings.Contains(line, "Bearer secret") || strings.Contains(line, "hidden") {
		t.Fatalf("secret leaked in log: %s", line)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["ts"] == nil || decoded["level"] != "INFO" || decoded["msg"] != "message" {
		t.Fatalf("missing structured fields: %#v", decoded)
	}
	if decoded["logger"] != "cyp.test" || decoded["password"] != Mask {
		t.Fatalf("logger metadata/redaction mismatch: %#v", decoded)
	}
}
