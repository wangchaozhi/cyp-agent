package api

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestRequestedMarketSymbols(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/market/history?symbol=btc%2Fusdt&symbol=ETH%2FUSDT%3AUSDT&symbol=BTC%2FUSDT", nil)
	symbols, err := requestedMarketSymbols(request, []string{"SOL/USDT"})
	if err != nil {
		t.Fatalf("requestedMarketSymbols() error = %v", err)
	}
	want := []string{"BTC/USDT", "ETH/USDT:USDT"}
	if !reflect.DeepEqual(symbols, want) {
		t.Fatalf("symbols = %v, want %v", symbols, want)
	}
}

func TestRequestedMarketSymbolsValidation(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/market/history?symbol=BTC", nil)
	if _, err := requestedMarketSymbols(request, nil); err == nil {
		t.Fatal("requestedMarketSymbols() accepted an invalid symbol")
	}

	request = httptest.NewRequest("GET", "/api/market/history?symbol=A%2FUSDT,B%2FUSDT,C%2FUSDT,D%2FUSDT,E%2FUSDT,F%2FUSDT,G%2FUSDT", nil)
	if _, err := requestedMarketSymbols(request, nil); err == nil {
		t.Fatal("requestedMarketSymbols() accepted more than the selection limit")
	}
}

func TestValidMarketTimeframe(t *testing.T) {
	for _, timeframe := range []string{"15m", "1h", "4h", "1d"} {
		if !validMarketTimeframe(timeframe) {
			t.Fatalf("validMarketTimeframe(%q) = false", timeframe)
		}
	}
	if validMarketTimeframe("2h") {
		t.Fatal("validMarketTimeframe accepted an unsupported timeframe")
	}
}
