package llm

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
)

const maxResponseBytes = 4 << 20

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("llm base URL must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("llm base URL must use HTTP or HTTPS")
	}
	if parsed.User != nil {
		return "", errors.New("llm base URL must not contain credentials")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func postJSON(
	ctx context.Context,
	client *http.Client,
	provider string,
	endpoint string,
	headers map[string]string,
	payload any,
	out any,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", provider, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return &ProviderError{Provider: provider, Operation: "request", Cause: err}
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		// NewRequest already validated the URL. Errors from Do are transport,
		// connection, redirect, or context failures and are safe to classify as
		// transient; the resilient client still stops immediately when its parent
		// context is canceled.
		return &ProviderError{Provider: provider, Operation: "request", Transient: true, Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Drain only a bounded amount to permit connection reuse, but never copy a
		// provider response body into the returned error or metrics.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return &ProviderError{
			Provider: provider, Operation: "request", StatusCode: response.StatusCode,
			Transient: response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500,
		}
	}
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return &ProviderError{Provider: provider, Operation: "read", Transient: true, Cause: err}
	}
	if len(body) > maxResponseBytes {
		return &ProviderError{Provider: provider, Operation: "read", Transient: true,
			Cause: errors.New("response exceeds size limit")}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &ProviderError{Provider: provider, Operation: "decode", Transient: true, Cause: err}
	}
	return nil
}
