package venue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

// newLiveOKX builds an adapter with explicitly enabled real trading. Demo is
// off, so requests must not carry the x-simulated-trading header.
func newLiveOKX(t *testing.T, baseURL string, configure func(*CEXConfig)) *CEXVenue {
	t.Helper()
	return newTestCEX(t, "okx", baseURL, func(config *CEXConfig) {
		config.APIKey, config.APISecret, config.Passphrase = "okx-live-key", "okx-live-secret", "pass"
		config.Demo, config.EnableLiveTrading = false, true
		config.RetryDelay = func(int, time.Duration) time.Duration { return 0 }
		if configure != nil {
			configure(config)
		}
	})
}

func okxLiveIntent(clientID string) contracts.OrderIntent {
	stop := contracts.MustDecimal("1800")
	return contracts.OrderIntent{
		ClientID: clientID, Symbol: "ETH/USDT:USDT", Venue: "okx",
		Side: contracts.SideLong, Instrument: contracts.InstrumentPerp,
		OrderType: contracts.EntryTypeMarket, SizeQuote: contracts.MustDecimal("200"),
		Leverage: 2, MarginMode: contracts.MarginModeIsolated, StopLoss: &stop,
	}
}

func TestOKXLiveConstructionGuards(t *testing.T) {
	t.Parallel()
	cases := map[string]CEXConfig{
		"binance can never trade live": {
			ExchangeID: "binance", APIKey: "k", APISecret: "s", EnableLiveTrading: true,
		},
		"demo account cannot be flagged live": {
			ExchangeID: "okx", APIKey: "k", APISecret: "s", Passphrase: "p",
			Demo: true, EnableLiveTrading: true,
		},
		"live trading requires full credentials": {
			ExchangeID: "okx", APIKey: "k", APISecret: "s", EnableLiveTrading: true,
		},
		"demo and live flags are mutually exclusive": {
			ExchangeID: "okx", APIKey: "k", APISecret: "s", Passphrase: "p",
			EnableDemoTrading: true, EnableLiveTrading: true,
		},
	}
	for name, config := range cases {
		if _, err := NewCEXVenue(config); err == nil {
			t.Fatalf("%s: constructor accepted an unsafe configuration", name)
		}
	}
	// Credentials alone never unlock trading without the explicit flag.
	readOnly, err := NewCEXVenue(CEXConfig{
		ExchangeID: "okx", APIKey: "k", APISecret: "s", Passphrase: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if readOnly.TradingEnabled() || !readOnly.Caps().ReadOnly {
		t.Fatal("credentials alone unlocked OKX trading")
	}
	identity := readOnly.ExecutionIdentity()
	if identity.Writable || identity.Environment != EnvironmentLive {
		t.Fatalf("read-only identity=%#v", identity)
	}
}

func TestOKXLiveReadinessRequiresSecureNetModeAccount(t *testing.T) {
	fixed := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		serverTime  time.Time
		accountJSON string
		want        string
	}{
		{name: "ready", serverTime: fixed, accountJSON: `{"acctLv":"2","posMode":"net_mode","perm":"read_only,trade","ip":"203.0.113.10"}`},
		{name: "clock skew", serverTime: fixed.Add(-3 * time.Second), accountJSON: `{"acctLv":"2","posMode":"net_mode","perm":"read_only,trade","ip":"203.0.113.10"}`, want: "clock differs"},
		{name: "hedge mode", serverTime: fixed, accountJSON: `{"acctLv":"2","posMode":"long_short_mode","perm":"read_only,trade","ip":"203.0.113.10"}`, want: "net_mode"},
		{name: "portfolio margin", serverTime: fixed, accountJSON: `{"acctLv":"4","posMode":"net_mode","perm":"read_only,trade","ip":"203.0.113.10"}`, want: "Portfolio"},
		{name: "missing trade", serverTime: fixed, accountJSON: `{"acctLv":"2","posMode":"net_mode","perm":"read_only","ip":"203.0.113.10"}`, want: "Trade permission"},
		{name: "withdraw forbidden", serverTime: fixed, accountJSON: `{"acctLv":"2","posMode":"net_mode","perm":"read_only,trade,withdraw","ip":"203.0.113.10"}`, want: "Withdraw"},
		{name: "ip required", serverTime: fixed, accountJSON: `{"acctLv":"2","posMode":"net_mode","perm":"read_only,trade","ip":""}`, want: "IP allowlist"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				switch request.URL.Path {
				case "/api/v5/public/time":
					_, _ = fmt.Fprintf(response, `{"code":"0","data":[{"ts":"%d"}]}`, test.serverTime.UnixMilli())
				case "/api/v5/account/config":
					_, _ = fmt.Fprintf(response, `{"code":"0","data":[%s]}`, test.accountJSON)
				default:
					http.NotFound(response, request)
				}
			}))
			defer server.Close()
			target := newLiveOKX(t, server.URL, func(config *CEXConfig) { config.Clock = func() time.Time { return fixed } })
			err := target.ValidateLiveReadiness(context.Background())
			if test.want == "" && err != nil {
				t.Fatalf("readiness rejected secure account: %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("readiness error=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestOKXLiveMutationFailsClosedWhenOwnershipLeaseIsLost(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	target := newLiveOKX(t, server.URL, nil)
	target.SetMutationGuard(func(context.Context) error { return errors.New("lease connection lost") })
	_, err := target.Place(context.Background(), okxLiveIntent("lease-lost"))
	if err == nil || !strings.Contains(err.Error(), "ownership lease") {
		t.Fatalf("mutation guard error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("exchange received %d requests after lease loss", requests.Load())
	}
}

func TestOKXLivePlacesWithoutSimulatedHeaderAndVerifiesProtection(t *testing.T) {
	var placed atomic.Int32
	protectClientID := SanitizeOKXClientID("live-entry", "protect")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.Header.Get("x-simulated-trading") != "" {
			t.Error("live request must not carry x-simulated-trading")
		}
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"instId":"ETH-USDT-SWAP","baseCcy":"ETH","quoteCcy":"USDT","settleCcy":"USDT","ctType":"linear","ctValCcy":"ETH","ctVal":"0.1","lotSz":"1","minSz":"1","tickSz":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"last":"2000"}]}`))
		case "/api/v5/account/config":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"posMode":"net_mode"}]}`))
		case "/api/v5/account/set-leverage":
			_, _ = response.Write([]byte(`{"code":"0","data":[{}]}`))
		case "/api/v5/trade/order":
			if request.Method == http.MethodPost {
				expires, err := strconv.ParseInt(request.Header.Get("expTime"), 10, 64)
				if err != nil || expires <= time.Now().UTC().UnixMilli() {
					t.Errorf("missing or expired OKX expTime header: %q", request.Header.Get("expTime"))
				}
				placed.Add(1)
				_, _ = response.Write([]byte(`{"code":"0","data":[{"ordId":"live-1","clOrdId":"liveentry","sCode":"0","sMsg":""}]}`))
				return
			}
			_, _ = response.Write([]byte(`{"code":"0","data":[{"ordId":"live-1","state":"filled","avgPx":"2001","accFillSz":"1","fee":"-1","feeCcy":"USDT"}]}`))
		case "/api/v5/trade/orders-algo-pending":
			_, _ = fmt.Fprintf(response, `{"code":"0","data":[{"algoId":"live-protect-1","algoClOrdId":%q,"side":"sell","posSide":"net","reduceOnly":"true","closeFraction":"1","slTriggerPx":"1800"}]}`, protectClientID)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	target := newLiveOKX(t, server.URL, func(config *CEXConfig) { config.Clock = time.Now })
	if !target.LiveTradingEnabled() || !target.TradingEnabled() || target.Caps().ReadOnly {
		t.Fatal("live adapter should be writable")
	}
	identity := target.ExecutionIdentity()
	if identity.Environment != EnvironmentLive || !identity.Writable || identity.VenueID != "okx" {
		t.Fatalf("live identity=%#v", identity)
	}
	result, err := target.Place(context.Background(), okxLiveIntent("live-entry"))
	if err != nil || result.Status != contracts.OrderStatusFilled || placed.Load() != 1 {
		t.Fatalf("live place result=%#v placed=%d err=%v", result, placed.Load(), err)
	}
	if !hasProtectiveKind(result.ProtectiveOrders, "stop_loss") ||
		result.ProtectiveOrders[0].OrderID != "live-protect-1" {
		t.Fatalf("live fill must carry exchange-verified protection: %#v", result.ProtectiveOrders)
	}
}

// TestOKXClockSkewSignatureRejectionNeverRetriesPOST injects OKX 50102
// ("timestamp expired", caused by local clock skew). Signature/timestamp
// errors classify as auth, which is not retryable and not ambiguous, so the
// adapter must fail immediately without a duplicate POST or a recovery query.
func TestOKXClockSkewSignatureRejectionNeverRetriesPOST(t *testing.T) {
	var placed atomic.Int32
	var recoveries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"instId":"ETH-USDT-SWAP","baseCcy":"ETH","quoteCcy":"USDT","settleCcy":"USDT","ctType":"linear","ctValCcy":"ETH","ctVal":"0.1","lotSz":"1","minSz":"1","tickSz":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"last":"2000"}]}`))
		case "/api/v5/account/config":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"posMode":"net_mode"}]}`))
		case "/api/v5/account/set-leverage":
			_, _ = response.Write([]byte(`{"code":"0","data":[{}]}`))
		case "/api/v5/trade/order":
			if request.Method == http.MethodPost {
				placed.Add(1)
				_, _ = response.Write([]byte(`{"code":"50102","msg":"Timestamp request expired","data":[]}`))
				return
			}
			recoveries.Add(1)
			_, _ = response.Write([]byte(`{"code":"0","data":[]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	target := newLiveOKX(t, server.URL, nil)
	_, err := target.Place(context.Background(), okxLiveIntent("skewed-clock"))
	var classified *CEXError
	if !errors.As(err, &classified) || classified.Kind != CEXErrorAuth || classified.Code != "50102" {
		t.Fatalf("clock skew error=%#v", err)
	}
	if errors.Is(err, ErrOrderStateUnknown) {
		t.Fatal("signature rejection is deterministic and must not be ambiguous")
	}
	if placed.Load() != 1 || recoveries.Load() != 0 {
		t.Fatalf("placed=%d recoveries=%d; POST must not retry and no recovery is needed", placed.Load(), recoveries.Load())
	}
}

func TestOKXAcknowledgedOrderWithLostSettlementStateIsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"instId":"ETH-USDT-SWAP","baseCcy":"ETH","quoteCcy":"USDT","settleCcy":"USDT","ctType":"linear","ctValCcy":"ETH","ctVal":"0.1","lotSz":"1","minSz":"1","tickSz":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"last":"2000"}]}`))
		case "/api/v5/account/config":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"posMode":"net_mode"}]}`))
		case "/api/v5/account/set-leverage":
			_, _ = response.Write([]byte(`{"code":"0","data":[{}]}`))
		case "/api/v5/trade/order":
			if request.Method == http.MethodPost {
				_, _ = response.Write([]byte(`{"code":"0","data":[{"ordId":"ack-then-lost","sCode":"0"}]}`))
				return
			}
			response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = response.Write([]byte(`{"code":"50004","msg":"API endpoint request timeout"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	target := newLiveOKX(t, server.URL, nil)
	_, err := target.Place(context.Background(), okxLiveIntent("ack-lost"))
	if !errors.Is(err, ErrOrderStateUnknown) {
		t.Fatalf("post-ack settlement error = %v, want ErrOrderStateUnknown", err)
	}
}

func TestOKXMalformedAcknowledgementRecoversByClientID(t *testing.T) {
	var recoveryQueries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"instId":"ETH-USDT-SWAP","baseCcy":"ETH","quoteCcy":"USDT","settleCcy":"USDT","ctType":"linear","ctValCcy":"ETH","ctVal":"0.1","lotSz":"1","minSz":"1","tickSz":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"last":"2000"}]}`))
		case "/api/v5/account/config":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"posMode":"net_mode"}]}`))
		case "/api/v5/account/set-leverage":
			_, _ = response.Write([]byte(`{"code":"0","data":[{}]}`))
		case "/api/v5/trade/order":
			if request.Method == http.MethodPost {
				_, _ = response.Write([]byte(`{"code":"0","data":[]}`))
				return
			}
			if recoveryQueries.Load() == 0 && request.URL.Query().Get("clOrdId") == "" {
				t.Error("malformed acknowledgement recovery omitted clOrdId")
			}
			recoveryQueries.Add(1)
			_, _ = response.Write([]byte(`{"code":"0","data":[{"ordId":"recovered-malformed","state":"canceled","accFillSz":"0"}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	target := newLiveOKX(t, server.URL, nil)
	result, err := target.Place(context.Background(), okxLiveIntent("malformed-ack"))
	if err != nil || result.Status != contracts.OrderStatusCanceled || recoveryQueries.Load() == 0 {
		t.Fatalf("malformed acknowledgement recovery result=%#v queries=%d err=%v", result, recoveryQueries.Load(), err)
	}
}

// TestOKXPartialFillCancelsRemainderWithoutProtectiveFalsePositive drives the
// order into partially_filled, verifies the remainder is canceled and
// authoritatively confirmed, and leaves protection remediation to the caller.
func TestOKXPartialFillCancelsRemainderWithoutProtectiveFalsePositive(t *testing.T) {
	var polls atomic.Int32
	var cancelCalls atomic.Int32
	var canceled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"instId":"ETH-USDT-SWAP","baseCcy":"ETH","quoteCcy":"USDT","settleCcy":"USDT","ctType":"linear","ctValCcy":"ETH","ctVal":"0.1","lotSz":"1","minSz":"1","tickSz":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"last":"2000"}]}`))
		case "/api/v5/account/config":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"posMode":"net_mode"}]}`))
		case "/api/v5/account/set-leverage":
			_, _ = response.Write([]byte(`{"code":"0","data":[{}]}`))
		case "/api/v5/trade/order":
			if request.Method == http.MethodPost {
				_, _ = response.Write([]byte(`{"code":"0","data":[{"ordId":"partial-1","sCode":"0","sMsg":""}]}`))
				return
			}
			polls.Add(1)
			state := "partially_filled"
			if canceled.Load() {
				state = "canceled"
			}
			_, _ = fmt.Fprintf(response, `{"code":"0","data":[{"ordId":"partial-1","state":%q,"avgPx":"2001","accFillSz":"3","fee":"-0.5","feeCcy":"USDT"}]}`, state)
		case "/api/v5/trade/cancel-order":
			cancelCalls.Add(1)
			canceled.Store(true)
			_, _ = response.Write([]byte(`{"code":"0","data":[{"ordId":"partial-1","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/orders-algo-pending":
			t.Error("partial fill must not trigger protective verification")
			_, _ = response.Write([]byte(`{"code":"0","data":[]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	target := newLiveOKX(t, server.URL, nil)
	result, err := target.Place(context.Background(), okxLiveIntent("partial-fill"))
	if err != nil || result.Status != contracts.OrderStatusPartiallyFilled {
		t.Fatalf("partial fill result=%#v err=%v", result, err)
	}
	if len(result.ProtectiveOrders) != 0 {
		t.Fatalf("partial fill reported protection: %#v", result.ProtectiveOrders)
	}
	if result.FilledBase.Cmp(contracts.MustDecimal("0.3")) != 0 {
		t.Fatalf("partial filled base=%s", result.FilledBase)
	}
	if polls.Load() != okxOrderPollAttempts+1 || cancelCalls.Load() != 1 {
		t.Fatalf("polls=%d cancelCalls=%d", polls.Load(), cancelCalls.Load())
	}
}

// TestOKXLiveRateLimitedPOSTReconcilesInsteadOfRetrying injects HTTP 429 on
// the order POST. The submission outcome is ambiguous (the exchange may have
// accepted it), so the adapter must reconcile by clOrdId instead of blindly
// retrying the POST.
func TestOKXLiveRateLimitedPOSTReconcilesInsteadOfRetrying(t *testing.T) {
	var placed atomic.Int32
	var lookups []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"instId":"ETH-USDT-SWAP","baseCcy":"ETH","quoteCcy":"USDT","settleCcy":"USDT","ctType":"linear","ctValCcy":"ETH","ctVal":"0.1","lotSz":"1","minSz":"1","tickSz":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"last":"2000"}]}`))
		case "/api/v5/account/config":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"posMode":"net_mode"}]}`))
		case "/api/v5/account/set-leverage":
			_, _ = response.Write([]byte(`{"code":"0","data":[{}]}`))
		case "/api/v5/trade/order":
			if request.Method == http.MethodPost {
				placed.Add(1)
				response.Header().Set("Retry-After", "1")
				response.WriteHeader(http.StatusTooManyRequests)
				_, _ = response.Write([]byte(`{"code":"50011","msg":"rate limited","data":[]}`))
				return
			}
			lookups = append(lookups, request.URL.RawQuery)
			_, _ = response.Write([]byte(`{"code":"0","data":[{"ordId":"limited-1","state":"filled","avgPx":"2000","accFillSz":"1","fee":"0","feeCcy":"USDT"}]}`))
		case "/api/v5/trade/orders-algo-pending":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"algoId":"limited-protect","slTriggerPx":"1800"}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	target := newLiveOKX(t, server.URL, nil)
	result, err := target.Place(context.Background(), okxLiveIntent("rate-limited"))
	if err != nil || result.Status != contracts.OrderStatusFilled || result.OrderID == nil || *result.OrderID != "limited-1" {
		t.Fatalf("rate limited recovery result=%#v err=%v", result, err)
	}
	if placed.Load() != 1 {
		t.Fatalf("POST retried under rate limit: placed=%d", placed.Load())
	}
	// The first status lookup is the ambiguity recovery and must key on the
	// deterministic clOrdId, never on an order ID we may not have received.
	if len(lookups) == 0 || !strings.Contains(lookups[0], "clOrdId=") {
		t.Fatalf("recovery lookups=%v", lookups)
	}
}

// TestOKXLiveDisconnectRecoversByClientID aborts the order POST mid-flight
// (network failure after the exchange may have accepted the order) and then
// restores the authoritative state via the deterministic clOrdId lookup.
func TestOKXLiveDisconnectRecoversByClientID(t *testing.T) {
	var placed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.Header.Get("x-simulated-trading") != "" {
			t.Error("live request must not carry x-simulated-trading")
		}
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"instId":"ETH-USDT-SWAP","baseCcy":"ETH","quoteCcy":"USDT","settleCcy":"USDT","ctType":"linear","ctValCcy":"ETH","ctVal":"0.1","lotSz":"1","minSz":"1","tickSz":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"last":"2000"}]}`))
		case "/api/v5/account/config":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"posMode":"net_mode"}]}`))
		case "/api/v5/account/set-leverage":
			_, _ = response.Write([]byte(`{"code":"0","data":[{}]}`))
		case "/api/v5/trade/order":
			if request.Method == http.MethodPost {
				placed.Add(1)
				panic(http.ErrAbortHandler)
			}
			_, _ = response.Write([]byte(`{"code":"0","data":[{"ordId":"reconnected-1","state":"filled","avgPx":"2000","accFillSz":"1","fee":"0","feeCcy":"USDT"}]}`))
		case "/api/v5/trade/orders-algo-pending":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"algoId":"reconnect-protect","slTriggerPx":"1800"}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	target := newLiveOKX(t, server.URL, nil)
	result, err := target.Place(context.Background(), okxLiveIntent("net-drop"))
	if err != nil || result.Status != contracts.OrderStatusFilled || result.OrderID == nil ||
		*result.OrderID != "reconnected-1" || placed.Load() != 1 {
		t.Fatalf("disconnect recovery result=%#v placed=%d err=%v", result, placed.Load(), err)
	}
	// Crash/restart replay: a fresh reconcile lookup (as run by startup
	// reconciliation) must find the same order without a new POST.
	reconciled, found, err := target.ReconcileOrder(context.Background(), okxLiveIntent("net-drop"))
	if err != nil || !found || reconciled.Status != contracts.OrderStatusFilled || placed.Load() != 1 {
		t.Fatalf("post-restart reconcile=%#v found=%t placed=%d err=%v", reconciled, found, placed.Load(), err)
	}
}

// TestOKXPlaceProtectiveOrdersRemediation exercises the standalone TP/SL algo
// endpoint used by the protective remediation path.
func TestOKXPlaceProtectiveOrdersRemediation(t *testing.T) {
	var algoBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v5/account/config":
			_, _ = response.Write([]byte(`{"code":"0","data":[{"posMode":"net_mode"}]}`))
		case "/api/v5/trade/order-algo":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			algoBodies = append(algoBodies, body)
			_, _ = response.Write([]byte(`{"code":"0","data":[{"algoId":"remediated-1","sCode":"0","sMsg":""}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	target := newLiveOKX(t, server.URL, nil)
	stop := contracts.MustDecimal("1800")
	take := contracts.MustDecimal("2200")
	if err := target.PlaceProtectiveOrders(context.Background(), "protect-entry", "ETH/USDT:USDT",
		contracts.SideLong, contracts.MarginModeIsolated, &stop, &take); err != nil {
		t.Fatal(err)
	}
	if len(algoBodies) != 1 {
		t.Fatalf("algo submissions=%d", len(algoBodies))
	}
	body := algoBodies[0]
	if body["instId"] != "ETH-USDT-SWAP" || body["ordType"] != "oco" || body["side"] != "sell" ||
		body["closeFraction"] != "1" || body["reduceOnly"] != true ||
		body["slTriggerPx"] != "1800" || body["tpTriggerPx"] != "2200" {
		t.Fatalf("algo body=%#v", body)
	}
	// Stop-loss only remediation downgrades to a single conditional order.
	if err := target.PlaceProtectiveOrders(context.Background(), "protect-short", "ETH/USDT:USDT",
		contracts.SideShort, contracts.MarginModeCross, &stop, nil); err != nil {
		t.Fatal(err)
	}
	body = algoBodies[1]
	if body["ordType"] != "conditional" || body["side"] != "buy" || body["tdMode"] != "cross" {
		t.Fatalf("conditional algo body=%#v", body)
	}
	// Missing prices and read-only adapters are rejected before any request.
	if err := target.PlaceProtectiveOrders(context.Background(), "invalid-protect", "ETH/USDT:USDT",
		contracts.SideLong, contracts.MarginModeIsolated, nil, nil); err == nil {
		t.Fatal("remediation without prices must fail")
	}
	readOnly := newTestCEX(t, "okx", server.URL, nil)
	if err := readOnly.PlaceProtectiveOrders(context.Background(), "readonly-protect", "ETH/USDT:USDT",
		contracts.SideLong, contracts.MarginModeIsolated, &stop, nil); !errors.Is(err, ErrCEXTradingDisabled) {
		t.Fatalf("read-only remediation error=%v", err)
	}
	if !strings.Contains(ErrCEXTradingDisabled.Error(), "OKX") {
		t.Fatal("disabled error should explain the allowed OKX paths")
	}
}
