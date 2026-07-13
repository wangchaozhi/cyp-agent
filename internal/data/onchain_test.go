package data

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func TestHTTPOnchainFetcherReadsMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != "BTC/USDT" {
			http.Error(w, "missing symbol", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"smart_money_flow":"1200000","liquidity_usd":"88000000","exchange_netflow":"-300000"}`))
	}))
	defer server.Close()

	fetcher, err := NewHTTPOnchainFetcher(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := fetcher(context.Background(), "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	if payload.SmartMoneyFlow == nil || payload.SmartMoneyFlow.String() != "1200000" {
		t.Fatalf("smart money flow = %+v", payload.SmartMoneyFlow)
	}
	if payload.ExchangeNetflow == nil || payload.ExchangeNetflow.String() != "-300000" {
		t.Fatalf("exchange netflow = %+v", payload.ExchangeNetflow)
	}
	if payload.HolderConcentration != nil {
		t.Fatalf("absent field must stay nil, got %+v", payload.HolderConcentration)
	}
}

func TestHTTPOnchainFetcherRejectsBadInput(t *testing.T) {
	if _, err := NewHTTPOnchainFetcher("ftp://example.com", nil); err == nil {
		t.Fatal("non-http URL must be rejected")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	fetcher, err := NewHTTPOnchainFetcher(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fetcher(context.Background(), "BTC/USDT"); err == nil {
		t.Fatal("HTTP 503 must surface as an error")
	}
	if _, err := fetcher(context.Background(), "  "); err == nil {
		t.Fatal("blank symbol must be rejected")
	}
}

type staticSource struct {
	snapshot contracts.MarketSnapshot
	err      error
}

func (source staticSource) Snapshot(context.Context, string) (contracts.MarketSnapshot, error) {
	return source.snapshot, source.err
}

func TestOnchainEnrichedSourceFillsOnlyMissingBlock(t *testing.T) {
	metric := contracts.MustDecimal("42")
	fetched := &contracts.OnchainData{SmartMoneyFlow: &metric}
	onchain := NewOnchainDataSource(func(context.Context, string) (*contracts.OnchainData, error) {
		return fetched, nil
	})

	enriched, err := NewOnchainEnrichedSource(staticSource{snapshot: contracts.MarketSnapshot{Symbol: "BTC/USDT"}}, onchain)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := enriched.Snapshot(context.Background(), "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Onchain == nil || snapshot.Onchain.SmartMoneyFlow.String() != "42" {
		t.Fatalf("enriched onchain = %+v", snapshot.Onchain)
	}

	existing := contracts.MustDecimal("7")
	withOnchain := contracts.MarketSnapshot{
		Symbol:  "BTC/USDT",
		Onchain: &contracts.OnchainData{SmartMoneyFlow: &existing},
	}
	enriched, err = NewOnchainEnrichedSource(staticSource{snapshot: withOnchain}, onchain)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = enriched.Snapshot(context.Background(), "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Onchain.SmartMoneyFlow.String() != "7" {
		t.Fatal("inner snapshot's onchain block must not be overwritten")
	}
}

func TestOnchainEnrichedSourceDegradesOnFetchFailure(t *testing.T) {
	onchain := NewOnchainDataSource(func(context.Context, string) (*contracts.OnchainData, error) {
		return nil, errors.New("indexer offline")
	})
	enriched, err := NewOnchainEnrichedSource(staticSource{snapshot: contracts.MarketSnapshot{Symbol: "BTC/USDT"}}, onchain)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := enriched.Snapshot(context.Background(), "BTC/USDT")
	if err != nil {
		t.Fatalf("fetch failure must not fail the snapshot: %v", err)
	}
	if snapshot.Onchain != nil {
		t.Fatalf("failed fetch must leave onchain nil, got %+v", snapshot.Onchain)
	}
}
