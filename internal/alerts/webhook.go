package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultWebhookTimeout = 5 * time.Second

type WebhookSink struct {
	url    string
	client *http.Client
}

func NewWebhookSink(rawURL string, client *http.Client) (*WebhookSink, error) {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("webhook URL must be an absolute http(s) URL")
	}
	if client == nil {
		client = &http.Client{Timeout: defaultWebhookTimeout}
	}
	return &WebhookSink{url: rawURL, client: client}, nil
}

func (sink *WebhookSink) Emit(ctx context.Context, alert Alert) error {
	if sink == nil || sink.client == nil {
		return errors.New("webhook sink is not configured")
	}
	payload, err := json.Marshal(alert)
	if err != nil {
		return fmt.Errorf("encode webhook alert: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, sink.url, bytes.NewReader(payload))
	if err != nil {
		return errors.New("create webhook request failed")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "cyp-agent-go/0.1")
	response, err := sink.client.Do(request)
	if err != nil {
		// net/http errors may embed the request URL, whose query often contains a
		// webhook token. Keep the cause out of logs and expose cancellation safely.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return errors.New("webhook request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", response.StatusCode)
	}
	return nil
}

var _ Sink = (*WebhookSink)(nil)
