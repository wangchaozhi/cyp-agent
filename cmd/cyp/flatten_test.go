package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeServer emulates the cyp-server REST surface used by `cyp flatten`.
type fakeServer struct {
	mu         sync.Mutex
	token      string
	kill       bool
	positions  []map[string]any
	closeCalls []string
	failClose  bool
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		if f.token != "" && r.Header.Get("X-CYP-API-Token") != f.token {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": "valid CYP API token required"})
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/positions", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(f.positions)
	})
	mux.HandleFunc("POST /api/killswitch", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		var payload struct {
			On bool `json:"on"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		f.mu.Lock()
		f.kill = payload.On
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]bool{"kill": payload.On})
	})
	mux.HandleFunc("POST /api/positions/close", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		var payload struct {
			Symbol     string `json:"symbol"`
			Instrument string `json:"instrument"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.closeCalls = append(f.closeCalls, payload.Symbol)
		if f.failClose {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": "injected close failure"})
			return
		}
		kept := f.positions[:0]
		for _, position := range f.positions {
			if position["symbol"] != payload.Symbol {
				kept = append(kept, position)
			}
		}
		f.positions = kept
		_ = json.NewEncoder(w).Encode(map[string]any{"closed": payload.Symbol})
	})
	return mux
}

func perpPosition(symbol string) map[string]any {
	return map[string]any{
		"symbol": symbol, "venue": "okx", "side": "long", "instrument": "perp",
		"size_base": "0.1", "mark_price": "2000", "notional": "200",
	}
}

func TestFlattenPreviewDoesNotTouchState(t *testing.T) {
	fake := &fakeServer{positions: []map[string]any{perpPosition("ETH/USDT:USDT")}}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	if err := runFlatten([]string{"-base", server.URL}); err != nil {
		t.Fatalf("preview run failed: %v", err)
	}
	if fake.kill || len(fake.closeCalls) != 0 {
		t.Fatalf("preview mutated state: kill=%t closes=%v", fake.kill, fake.closeCalls)
	}
}

func TestFlattenClosesEverythingAndEngagesKill(t *testing.T) {
	fake := &fakeServer{
		token: "secret",
		positions: []map[string]any{
			perpPosition("ETH/USDT:USDT"), perpPosition("BTC/USDT:USDT"),
		},
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	if err := runFlatten([]string{"-base", server.URL, "-token", "secret", "-yes"}); err != nil {
		t.Fatalf("flatten failed: %v", err)
	}
	if !fake.kill {
		t.Fatal("kill switch was not engaged before flattening")
	}
	if len(fake.closeCalls) != 2 || len(fake.positions) != 0 {
		t.Fatalf("closes=%v remaining=%v", fake.closeCalls, fake.positions)
	}
}

func TestFlattenReportsFailuresAndRemainingPositions(t *testing.T) {
	fake := &fakeServer{
		positions: []map[string]any{perpPosition("ETH/USDT:USDT")},
		failClose: true,
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	err := runFlatten([]string{"-base", server.URL, "-yes"})
	if err == nil || !strings.Contains(err.Error(), "injected close failure") ||
		!strings.Contains(err.Error(), "仍有持仓") {
		t.Fatalf("failure surface = %v", err)
	}
}

func TestFlattenRejectsMissingToken(t *testing.T) {
	fake := &fakeServer{token: "secret", positions: []map[string]any{perpPosition("ETH/USDT:USDT")}}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	err := runFlatten([]string{"-base", server.URL, "-yes"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("unauthorized error = %v", err)
	}
}
