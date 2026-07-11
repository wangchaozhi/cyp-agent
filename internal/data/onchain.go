package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

const maxOnchainResponseBytes = 1 << 20

// NewHTTPOnchainFetcher builds a read-only fetcher against an external
// onchain metrics API. The endpoint receives GET {base}?symbol=... and must
// answer with a JSON object matching contracts.OnchainData. Fetch failures
// degrade the onchain analyst instead of failing the snapshot.
func NewHTTPOnchainFetcher(baseURL string, client *http.Client) (OnchainFetcher, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("onchain data API must be an http(s) URL, got %q", baseURL)
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	endpoint := *parsed
	return func(ctx context.Context, symbol string) (*contracts.OnchainData, error) {
		symbol = strings.TrimSpace(symbol)
		if symbol == "" {
			return nil, errors.New("onchain fetch requires a symbol")
		}
		target := endpoint
		query := target.Query()
		query.Set("symbol", symbol)
		target.RawQuery = query.Encode()

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("onchain data API returned HTTP %d", response.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, maxOnchainResponseBytes))
		if err != nil {
			return nil, err
		}
		var payload contracts.OnchainData
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode onchain payload: %w", err)
		}
		return &payload, nil
	}, nil
}

// OnchainEnrichedSource decorates a market data source with read-only onchain
// metrics. The underlying snapshot always wins: only a missing Onchain block
// is filled in, and fetch errors never fail the snapshot.
type OnchainEnrichedSource struct {
	inner   Source
	onchain *OnchainDataSource
}

func NewOnchainEnrichedSource(inner Source, onchain *OnchainDataSource) (*OnchainEnrichedSource, error) {
	if inner == nil {
		return nil, errors.New("onchain enrichment requires an inner market source")
	}
	return &OnchainEnrichedSource{inner: inner, onchain: onchain}, nil
}

func (source *OnchainEnrichedSource) Snapshot(
	ctx context.Context,
	symbol string,
) (contracts.MarketSnapshot, error) {
	snapshot, err := source.inner.Snapshot(ctx, symbol)
	if err != nil {
		return snapshot, err
	}
	if snapshot.Onchain == nil && source.onchain.IsConfigured() {
		snapshot.Onchain = source.onchain.Fetch(ctx, symbol)
	}
	return snapshot, nil
}
