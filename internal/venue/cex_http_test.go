package venue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func newTestCEX(t *testing.T, exchange, baseURL string, configure func(*CEXConfig)) *CEXVenue {
	t.Helper()
	config := CEXConfig{
		ExchangeID: exchange, BaseURL: baseURL, HTTPClient: &http.Client{Timeout: time.Second},
		Clock: func() time.Time { return time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC) },
	}
	if configure != nil {
		configure(&config)
	}
	venue, err := NewCEXVenue(config)
	if err != nil {
		t.Fatal(err)
	}
	return venue
}

func TestBinancePublicMarketEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Errorf("symbol=%q", request.URL.Query().Get("symbol"))
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v3/ticker/price":
			_, _ = response.Write([]byte(`{"symbol":"BTCUSDT","price":"60000.12345678"}`))
		case "/api/v3/klines":
			_, _ = response.Write([]byte(`[[2000,"101","103","100","102","12.5"],[1000,"100","102","99","101","10.5"]]`))
		case "/api/v3/depth":
			_, _ = response.Write([]byte(`{"bids":[["59999.1","1.2"]],"asks":[["60000.2","0.8"]]}`))
		case "/fapi/v1/premiumIndex":
			_, _ = response.Write([]byte(`{"lastFundingRate":"0.0004"}`))
		case "/fapi/v1/openInterest":
			_, _ = response.Write([]byte(`{"openInterest":"123456.789"}`))
		case "/futures/data/globalLongShortAccountRatio":
			_, _ = response.Write([]byte(`[{"longShortRatio":"1.25"}]`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	venue := newTestCEX(t, "binance", server.URL, nil)
	ctx := context.Background()
	price, err := venue.FetchTicker(ctx, "BTC/USDT")
	if err != nil || price.Cmp(contracts.MustDecimal("60000.12345678")) != 0 {
		t.Fatalf("ticker=%s err=%v", price, err)
	}
	candles, err := venue.FetchOHLCV(ctx, "BTC/USDT", "1h", 2)
	if err != nil || len(candles) != 2 || !candles[0].TS.Before(candles[1].TS) ||
		candles[0].Close.Cmp(contracts.MustDecimal("101")) != 0 {
		t.Fatalf("candles=%#v err=%v", candles, err)
	}
	book, err := venue.FetchOrderBook(ctx, "BTC/USDT", 20)
	if err != nil || len(book.Bids) != 1 || book.Bids[0][0].Cmp(contracts.MustDecimal("59999.1")) != 0 {
		t.Fatalf("book=%#v err=%v", book, err)
	}
	funding, err := venue.FetchFundingRate(ctx, "BTC/USDT:USDT")
	if err != nil || funding.Cmp(contracts.MustDecimal("0.0004")) != 0 {
		t.Fatalf("funding=%s err=%v", funding, err)
	}
	interest, err := venue.FetchOpenInterest(ctx, "BTC/USDT:USDT")
	if err != nil || interest.Cmp(contracts.MustDecimal("123456.789")) != 0 {
		t.Fatalf("interest=%s err=%v", interest, err)
	}
	ratio, err := venue.FetchLongShortRatio(ctx, "BTC/USDT:USDT")
	if err != nil || ratio.Cmp(contracts.MustDecimal("1.25")) != 0 {
		t.Fatalf("ratio=%s err=%v", ratio, err)
	}
}

func TestBinanceSignedReadOnlyAccountRequests(t *testing.T) {
	const secret = "binance-secret"
	var signedCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Errorf("missing API key header")
		}
		query := request.URL.Query()
		signature := query.Get("signature")
		query.Del("signature")
		if signature != hmacHex(secret, query.Encode()) {
			t.Errorf("bad signature %q over %q", signature, query.Encode())
		}
		signedCalls.Add(1)
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v3/account":
			_, _ = response.Write([]byte(`{"balances":[{"asset":"USDT","free":"123.45","locked":"6.55"}]}`))
		case "/fapi/v2/positionRisk":
			_, _ = response.Write([]byte(`[{"symbol":"BTCUSDT","positionAmt":"-0.25","entryPrice":"60000","leverage":"3","liquidationPrice":"79000","marginType":"isolated"}]`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	venue := newTestCEX(t, "binance", server.URL, func(config *CEXConfig) {
		config.APIKey, config.APISecret = "binance-key", secret
	})
	balances, err := venue.Balances(context.Background())
	if err != nil || balances.FreeQuote.Cmp(contracts.MustDecimal("123.45")) != 0 ||
		balances.TotalQuote.Cmp(contracts.MustDecimal("130.00")) != 0 {
		t.Fatalf("balances=%#v err=%v", balances, err)
	}
	positions, err := venue.Positions(context.Background())
	if err != nil || len(positions) != 1 || positions[0].Side != contracts.SideShort ||
		positions[0].SizeBase.Cmp(contracts.MustDecimal("0.25")) != 0 {
		t.Fatalf("positions=%#v err=%v", positions, err)
	}
	if signedCalls.Load() != 2 {
		t.Fatalf("signed calls=%d", signedCalls.Load())
	}
}

func TestOKXPublicMarketAndDemoSignature(t *testing.T) {
	const secret = "okx-secret"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v5/market/ticker":
			if request.URL.Query().Get("instId") != "BTC-USDT" {
				t.Errorf("instId=%q", request.URL.Query().Get("instId"))
			}
			_, _ = response.Write([]byte(`{"code":"0","data":[{"last":"60000.01"}]}`))
		case "/api/v5/market/candles":
			_, _ = response.Write([]byte(`{"code":"0","data":[["2000","101","103","100","102","12"],["1000","100","102","99","101","10"]]}`))
		case "/api/v5/market/books":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"bids":[["59999","1","0","1"]],"asks":[["60001","2","0","1"]]}]}`))
		case "/api/v5/public/funding-rate":
			if request.URL.Query().Get("instId") != "BTC-USDT-SWAP" {
				t.Errorf("swap instId=%q", request.URL.Query().Get("instId"))
			}
			_, _ = response.Write([]byte(`{"code":"0","data":[{"fundingRate":"0.0003"}]}`))
		case "/api/v5/public/open-interest":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"oiCcy":"8888.25","oi":"1"}]}`))
		case "/api/v5/account/balance":
			if request.Header.Get("x-simulated-trading") != "1" {
				t.Error("missing demo header")
			}
			timestamp := request.Header.Get("OK-ACCESS-TIMESTAMP")
			prehash := timestamp + "GET" + request.URL.RequestURI()
			if request.Header.Get("OK-ACCESS-SIGN") != hmacBase64(secret, prehash) {
				t.Error("bad OKX signature")
			}
			_, _ = response.Write([]byte(`{"code":"0","data":[{"details":[{"ccy":"USDT","availBal":"90","eq":"100","cashBal":"90"}]}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	venue := newTestCEX(t, "okx", server.URL, func(config *CEXConfig) {
		config.APIKey, config.APISecret, config.Passphrase, config.Demo = "okx-key", secret, "pass", true
	})
	ctx := context.Background()
	price, err := venue.FetchTicker(ctx, "BTC/USDT")
	if err != nil || price.Cmp(contracts.MustDecimal("60000.01")) != 0 {
		t.Fatalf("ticker=%s err=%v", price, err)
	}
	candles, err := venue.FetchOHLCV(ctx, "BTC/USDT", "1h", 2)
	if err != nil || len(candles) != 2 || !candles[0].TS.Before(candles[1].TS) {
		t.Fatalf("candles=%#v err=%v", candles, err)
	}
	book, err := venue.FetchOrderBook(ctx, "BTC/USDT", 20)
	if err != nil || len(book.Asks) != 1 {
		t.Fatalf("book=%#v err=%v", book, err)
	}
	funding, err := venue.FetchFundingRate(ctx, "BTC/USDT:USDT")
	if err != nil || funding.Cmp(contracts.MustDecimal("0.0003")) != 0 {
		t.Fatalf("funding=%s err=%v", funding, err)
	}
	interest, err := venue.FetchOpenInterest(ctx, "BTC/USDT:USDT")
	if err != nil || interest.Cmp(contracts.MustDecimal("8888.25")) != 0 {
		t.Fatalf("interest=%s err=%v", interest, err)
	}
	balances, err := venue.Balances(ctx)
	if err != nil || balances.TotalQuote.Cmp(contracts.MustDecimal("100")) != 0 {
		t.Fatalf("balances=%#v err=%v", balances, err)
	}
}

func TestOKXDemoPlacesPerpetualWithNativeProtection(t *testing.T) {
	const secret = "okx-demo-secret"
	var placed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(request.URL.Path, "/api/v5/account/") || strings.HasPrefix(request.URL.Path, "/api/v5/trade/") {
			if request.Header.Get("x-simulated-trading") != "1" {
				t.Error("private Demo request is missing x-simulated-trading=1")
			}
			if request.Header.Get("OK-ACCESS-KEY") != "okx-demo-key" {
				t.Error("private Demo request is missing API key")
			}
		}
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			if request.URL.Query().Get("instId") != "ETH-USDT-SWAP" {
				t.Errorf("instrument instId=%q", request.URL.Query().Get("instId"))
			}
			_, _ = response.Write([]byte(`{"code":"0","data":[{"instId":"ETH-USDT-SWAP","baseCcy":"","quoteCcy":"","settleCcy":"USDT","ctType":"linear","ctValCcy":"ETH","ctVal":"0.1","lotSz":"1","minSz":"1","tickSz":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"last":"2000"}]}`))
		case "/api/v5/account/config":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"posMode":"long_short_mode"}]}`))
		case "/api/v5/account/set-leverage":
			assertOKXDemoPOSTSignature(t, request, secret)
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["instId"] != "ETH-USDT-SWAP" || body["lever"] != "2" || body["posSide"] != "long" {
				t.Errorf("set leverage body=%#v", body)
			}
			_, _ = response.Write([]byte(`{"code":"0","data":[{}]}`))
		case "/api/v5/trade/order":
			if request.Method == http.MethodPost {
				placed.Add(1)
				raw := assertOKXDemoPOSTSignature(t, request, secret)
				var body map[string]any
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Error(err)
				}
				attached, _ := body["attachAlgoOrds"].([]any)
				if body["instId"] != "ETH-USDT-SWAP" || body["side"] != "buy" ||
					body["posSide"] != "long" || body["sz"] != "10" || len(attached) != 1 {
					t.Errorf("order body=%#v", body)
				}
				_, _ = response.Write([]byte(`{"code":"0","data":[{"ordId":"demo-order-1","clOrdId":"runethdemo","sCode":"0","sMsg":""}]}`))
				return
			}
			_, _ = response.Write([]byte(`{"code":"0","data":[{"ordId":"demo-order-1","clOrdId":"runethdemo","state":"filled","avgPx":"2001","accFillSz":"10","fee":"-2","feeCcy":"USDT"}]}`))
		case "/api/v5/trade/orders-algo-pending":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"algoId":"protect-1","slTriggerPx":"1800","tpTriggerPx":"2200"}]}`))
		case "/api/v5/account/positions":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"instId":"ETH-USDT-SWAP","pos":"10","posSide":"long","avgPx":"2001","lever":"2","liqPx":"1100","mgnMode":"isolated"}]}`))
		case "/api/v5/trade/cancel-order":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"ordId":"demo-order-1","sCode":"0","sMsg":""}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	target := newTestCEX(t, "okx", server.URL, func(config *CEXConfig) {
		config.APIKey, config.APISecret, config.Passphrase = "okx-demo-key", secret, "pass"
		config.Demo, config.EnableDemoTrading = true, true
	})
	if target.Caps().ReadOnly || !target.DemoTradingEnabled() {
		t.Fatal("explicit OKX Demo execution adapter should be writable")
	}
	stop := contracts.MustDecimal("1800")
	intent := contracts.OrderIntent{
		ClientID: "run-eth-demo", Symbol: "ETH/USDT:USDT", Venue: "okx",
		Side: contracts.SideLong, Instrument: contracts.InstrumentPerp,
		OrderType: contracts.EntryTypeMarket, SizeQuote: contracts.MustDecimal("2000"),
		Leverage: 2, MarginMode: contracts.MarginModeIsolated, StopLoss: &stop,
		TakeProfit: contracts.List[contracts.Decimal]{contracts.MustDecimal("2200")},
	}
	result, err := target.Place(context.Background(), intent)
	if err != nil || result.Status != contracts.OrderStatusFilled || result.FilledBase.Cmp(contracts.MustDecimal("1")) != 0 ||
		result.AvgPrice == nil || result.AvgPrice.Cmp(contracts.MustDecimal("2001")) != 0 || len(result.ProtectiveOrders) != 2 {
		t.Fatalf("place result=%#v err=%v", result, err)
	}
	replayed, err := target.Place(context.Background(), intent)
	if err != nil || replayed.Status != contracts.OrderStatusFilled || placed.Load() != 1 {
		t.Fatalf("idempotent replay=%#v placed=%d err=%v", replayed, placed.Load(), err)
	}
	wrongVenue := intent
	wrongVenue.ClientID = "wrong-venue"
	wrongVenue.Venue = "paper"
	rejected, err := target.Place(context.Background(), wrongVenue)
	if err != nil || rejected.Status != contracts.OrderStatusRejected || placed.Load() != 1 {
		t.Fatalf("misrouted order=%#v placed=%d err=%v", rejected, placed.Load(), err)
	}
	protective, err := target.ProtectiveOrders(context.Background(), intent.Symbol)
	if err != nil || !hasProtectiveKind(protective, "stop_loss") || !hasProtectiveKind(protective, "take_profit") {
		t.Fatalf("protective=%#v err=%v", protective, err)
	}
	positions, err := target.Positions(context.Background())
	if err != nil || len(positions) != 1 || positions[0].SizeBase.Cmp(contracts.MustDecimal("1")) != 0 {
		t.Fatalf("positions=%#v err=%v", positions, err)
	}
	if err := target.Cancel(context.Background(), intent.ClientID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

func assertOKXDemoPOSTSignature(t *testing.T, request *http.Request, secret string) []byte {
	t.Helper()
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	timestamp := request.Header.Get("OK-ACCESS-TIMESTAMP")
	prehash := timestamp + request.Method + request.URL.RequestURI() + string(raw)
	if request.Header.Get("OK-ACCESS-SIGN") != hmacBase64(secret, prehash) {
		t.Errorf("bad OKX Demo POST signature")
	}
	request.Body = io.NopCloser(strings.NewReader(string(raw)))
	return raw
}

func hasProtectiveKind(orders []contracts.ProtectiveOrder, kind string) bool {
	for _, order := range orders {
		if order.Kind == kind && order.ReduceOnly && order.TriggerPrice.IsPositive() {
			return true
		}
	}
	return false
}

func TestCEXErrorsPrecisionAdaptersAndTradingGuard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Retry-After", "3")
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{"code":-1003,"msg":"too many requests"}`))
	}))
	defer server.Close()
	venue := newTestCEX(t, "binance", server.URL, nil)
	_, err := venue.FetchTicker(context.Background(), "BTC/USDT")
	var classified *CEXError
	if !errors.As(err, &classified) || classified.Kind != CEXErrorRateLimit ||
		!classified.Retryable() || classified.RetryAfter != 3*time.Second {
		t.Fatalf("error=%#v", err)
	}

	quantized, err := QuantizeDown(contracts.MustDecimal("1.234567"), contracts.MustDecimal("0.001"))
	if err != nil || quantized.Cmp(contracts.MustDecimal("1.234")) != 0 {
		t.Fatalf("quantized=%s err=%v", quantized, err)
	}
	if got := SanitizeOKXClientID("run-1_with/symbols", "-tp0"); got != "run1withsymbolstp0" {
		t.Fatalf("sanitized=%q", got)
	}
	intent := contracts.OrderIntent{
		ClientID: "run-1", Instrument: contracts.InstrumentPerp, MarginMode: contracts.MarginModeIsolated,
	}
	okx := newTestCEX(t, "okx", server.URL, nil)
	params := okx.EntryParameters(intent, true)
	if params["tdMode"] != "isolated" || params["reduceOnly"] != true {
		t.Fatalf("params=%#v", params)
	}

	result, placeErr := venue.Place(context.Background(), contracts.OrderIntent{ClientID: "no-trade"})
	if placeErr != nil || result.Status != contracts.OrderStatusRejected || result.Error == nil ||
		!strings.Contains(*result.Error, "硬禁用") {
		t.Fatalf("place=%#v err=%v", result, placeErr)
	}
	if cancelErr := venue.Cancel(context.Background(), "order"); !errors.Is(cancelErr, ErrCEXTradingDisabled) {
		t.Fatalf("cancel error=%v", cancelErr)
	}
}

func TestOKXAPIErrorClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"code":"50113","msg":"invalid signature","data":[]}`))
	}))
	defer server.Close()
	venue := newTestCEX(t, "okx", server.URL, nil)
	_, err := venue.FetchTicker(context.Background(), "BTC/USDT")
	var classified *CEXError
	if !errors.As(err, &classified) || classified.Kind != CEXErrorAuth || classified.Code != "50113" {
		t.Fatalf("error=%#v", err)
	}
}

func TestSignedRequestDoesNotMutateCallerQuery(t *testing.T) {
	venue := newTestCEX(t, "binance", "http://example.test", func(config *CEXConfig) {
		config.APIKey, config.APISecret = "key", "secret"
	})
	query := url.Values{"symbol": {"BTCUSDT"}}
	request, err := venue.NewHTTPRequest(context.Background(), http.MethodGet, "/api/v3/account", query, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("signature") != "" || query.Get("timestamp") != "" {
		t.Fatalf("caller query mutated: %v", query)
	}
	if request.URL.Query().Get("signature") == "" || request.Header.Get("X-MBX-APIKEY") != "key" {
		t.Fatalf("unsigned request: %s", request.URL.String())
	}
	if strings.Contains(fmt.Sprint(venue), "secret") {
		t.Fatal("venue formatting leaked secret")
	}
}
