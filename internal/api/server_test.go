package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/api"
	"github.com/wangchaozhi/cyp-agent/internal/app"
	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type bullishSource struct{}

func (bullishSource) Snapshot(ctx context.Context, symbol string) (contracts.MarketSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return contracts.MarketSnapshot{}, err
	}
	candles := make(contracts.List[contracts.Candle], 80)
	for index := range candles {
		price := contracts.NewDecimalFromInt64(int64(100 + index*index))
		candles[index] = contracts.Candle{
			TS: time.Unix(int64(index*3600), 0).UTC(), Open: price, High: price,
			Low: price, Close: price, Volume: contracts.MustDecimal("100"),
		}
	}
	return contracts.MarketSnapshot{
		Symbol: symbol, Venue: "test", TS: time.Now().UTC(), OHLCV: candles,
	}, nil
}

func newTestApplication(t *testing.T, mutate func(*config.Settings)) (*app.Application, *httptest.Server) {
	t.Helper()
	settings := config.DefaultSettings()
	settings.Approval = "dashboard"
	settings.Persistence = "memory"
	settings.OHLCVArchiveEnabled = false
	settings.TokenUsageEnabled = false
	settings.Risk.ApprovalTimeoutSeconds = 3
	if mutate != nil {
		mutate(&settings)
	}
	application, err := app.New(context.Background(), settings, "", nil, app.WithDataSource(bullishSource{}))
	if err != nil {
		t.Fatalf("app.New() error = %v", err)
	}
	server := httptest.NewServer(application.API.Handler())
	t.Cleanup(func() {
		server.Close()
		application.Close()
	})
	return application, server
}

func requestJSON(t *testing.T, client *http.Client, method, url string, payload any) (*http.Response, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	return response, data
}

func TestHealthSettingsKillAndDashboardShapes(t *testing.T) {
	_, server := newTestApplication(t, func(settings *config.Settings) {
		settings.LLMProvider = "deepseek"
		settings.DeepSeekAPIKey = config.Secret("deepseek-secret")
		settings.OKXAPIKey = config.Secret("okx-key")
		settings.OKXAPISecret = config.Secret("okx-secret")
		settings.OKXPassword = config.Secret("okx-pass")
	})
	client := server.Client()

	for _, path := range []string{"/api/health", "/api/venues", "/api/settings", "/api/risk", "/api/portfolio", "/api/market", "/api/metrics", "/api/token-usage", "/api/pending", "/api/trades"} {
		response, body := requestJSON(t, client, http.MethodGet, server.URL+path, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", path, response.StatusCode, body)
		}
		if !json.Valid(body) {
			t.Fatalf("GET %s returned invalid JSON: %s", path, body)
		}
		if strings.Contains(string(body), "deepseek-secret") || strings.Contains(string(body), "okx-secret") || strings.Contains(string(body), "okx-pass") {
			t.Fatalf("GET %s leaked a secret: %s", path, body)
		}
	}

	response, body := requestJSON(t, client, http.MethodPost, server.URL+"/api/settings", map[string]any{
		"watchlist": []string{"BTC/USDT", "ETH/USDT"},
	})
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"watchlist":["BTC/USDT","ETH/USDT"]`) {
		t.Fatalf("watchlist settings response = %d %s", response.StatusCode, body)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/api/settings", map[string]any{
		"scan_interval": 600,
	})
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"intervals":{"scan":600`) {
		t.Fatalf("scan interval settings response = %d %s", response.StatusCode, body)
	}
	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/api/settings", map[string]any{
		"scan_interval": 90,
	})
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "scan_interval") {
		t.Fatalf("invalid scan interval response = %d %s", response.StatusCode, body)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/api/killswitch", map[string]any{"on": true})
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"kill":true`) {
		t.Fatalf("killswitch response = %d %s", response.StatusCode, body)
	}
	response, body = requestJSON(t, client, http.MethodGet, server.URL+"/api/health", nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"kill":true`) {
		t.Fatalf("health did not reflect killswitch: %d %s", response.StatusCode, body)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/api/settings", map[string]any{
		"automation": map[string]any{
			"enabled": true, "scan_enabled": false, "approval_enabled": false, "exit_enabled": false,
			"max_risk_score": 0.4, "max_quote": "150", "min_confidence": 0.7,
		},
	})
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"automation":{"enabled":true`) ||
		!strings.Contains(string(body), `"max_quote":"150"`) {
		t.Fatalf("automation settings response = %d %s", response.StatusCode, body)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/api/settings", map[string]any{
		"mode": "live", "automation": map[string]any{"enabled": true},
	})
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "automation") {
		t.Fatalf("live mode must reject explicitly enabled automation: %d %s", response.StatusCode, body)
	}
	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/api/settings", map[string]any{"mode": "live"})
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"mode":"live"`) || !strings.Contains(string(body), `"live_guard":{"ok":false`) {
		t.Fatalf("mode switch response = %d %s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), `"automation":{"enabled":false`) {
		t.Fatalf("live mode did not disable inherited automation: %d %s", response.StatusCode, body)
	}
	response, body = requestJSON(t, client, http.MethodGet, server.URL+"/api/health", nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"mode":"live"`) {
		t.Fatalf("health did not reflect mode switch: %d %s", response.StatusCode, body)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/api/settings", map[string]any{"llm_provider": "invalid"})
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), `"detail"`) {
		t.Fatalf("invalid settings response = %d %s", response.StatusCode, body)
	}
}

func TestMutationAuthenticationAndBrowserOriginGuard(t *testing.T) {
	_, server := newTestApplication(t, func(settings *config.Settings) {
		settings.APIToken = config.Secret("write-secret")
	})
	client := server.Client()

	preflight, err := http.NewRequest(http.MethodOptions, server.URL+"/api/killswitch", nil)
	if err != nil {
		t.Fatal(err)
	}
	preflight.Header.Set("Origin", "http://localhost:5173")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	preflight.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	response, err := client.Do(preflight)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent || response.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("CORS preflight = %d, headers = %#v", response.StatusCode, response.Header)
	}
	response.Body.Close()

	response, body := requestJSON(t, client, http.MethodPost, server.URL+"/api/killswitch", map[string]any{"on": true})
	if response.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "token") {
		t.Fatalf("unauthenticated mutation = %d %s", response.StatusCode, body)
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/killswitch", strings.NewReader(`{"on":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer write-secret")
	request.Header.Set("Origin", "https://attacker.invalid")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin mutation = %d %s", response.StatusCode, body)
	}

	request, err = http.NewRequest(http.MethodPost, server.URL+"/api/killswitch", strings.NewReader(`{"on":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer write-secret")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"kill":true`) {
		t.Fatalf("authenticated mutation = %d %s", response.StatusCode, body)
	}
}

func TestBacktestCompatibilityAndValidation(t *testing.T) {
	_, server := newTestApplication(t, nil)
	payload := map[string]any{
		"symbol": "BTC/USDT", "bars": 120, "window": 30, "seed": 11,
		"drift": 0.001, "vol": 0.01, "data": "synthetic", "timeframe": "1h",
	}
	response, body := requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/backtest", payload)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("backtest status = %d, body = %s", response.StatusCode, body)
	}
	var report map[string]any
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if report["symbol"] != "BTC/USDT" || report["n_bars"] != float64(120) {
		t.Fatalf("unexpected report: %#v", report)
	}
	if _, ok := report["equity_curve"].([]any); !ok {
		t.Fatalf("equity_curve is not an array: %#v", report["equity_curve"])
	}

	payload["window"] = 120
	response, body = requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/backtest", payload)
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "window must be smaller") {
		t.Fatalf("invalid backtest response = %d %s", response.StatusCode, body)
	}
}

func TestFullHTTPApprovalAndCloseLoop(t *testing.T) {
	application, server := newTestApplication(t, func(settings *config.Settings) {
		settings.Automation.Enabled = false
	})
	client := server.Client()
	response, body := requestJSON(t, client, http.MethodPost, server.URL+"/api/run", map[string]any{"symbol": "BTC/USDT"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("run status = %d, body = %s", response.StatusCode, body)
	}
	var accepted struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(body, &accepted); err != nil || len(accepted.RunID) != 12 {
		t.Fatalf("unexpected accepted run: %s (%v)", body, err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, body = requestJSON(t, client, http.MethodGet, server.URL+"/api/pending", nil)
		if strings.Contains(string(body), accepted.RunID) {
			break
		}
		if time.Now().After(deadline) {
			_, runBody := requestJSON(t, client, http.MethodGet, server.URL+"/api/runs/"+accepted.RunID, nil)
			t.Fatalf("run never reached pending approval: pending=%s run=%s", body, runBody)
		}
		time.Sleep(10 * time.Millisecond)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/api/approvals/"+accepted.RunID, map[string]any{"decision": "approve"})
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("approve response = %d %s", response.StatusCode, body)
	}

	deadline = time.Now().Add(2 * time.Second)
	var positions []map[string]any
	for {
		_, body = requestJSON(t, client, http.MethodGet, server.URL+"/api/positions", nil)
		if err := json.Unmarshal(body, &positions); err != nil {
			t.Fatalf("decode positions: %v: %s", err, body)
		}
		if len(positions) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("approved run never opened position: %s", body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, field := range []string{"mark_price", "notional", "unrealized_pnl", "unrealized_pnl_pct"} {
		if _, ok := positions[0][field].(string); !ok {
			t.Fatalf("position field %s is not a decimal string: %#v", field, positions[0][field])
		}
	}
	for {
		_, body = requestJSON(t, client, http.MethodGet, server.URL+"/api/risk", nil)
		var riskSnapshot struct {
			OrdersLastHour int    `json:"orders_last_hour"`
			RealizedPNL    string `json:"realized_pnl"`
		}
		if err := json.Unmarshal(body, &riskSnapshot); err != nil {
			t.Fatalf("decode risk snapshot: %v: %s", err, body)
		}
		if riskSnapshot.OrdersLastHour == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("risk state did not record opening order: %s", body)
		}
		time.Sleep(10 * time.Millisecond)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/api/positions/close", map[string]any{"symbol": "BTC/USDT", "instrument": "spot"})
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"filled"`) {
		t.Fatalf("close response = %d %s", response.StatusCode, body)
	}
	response, body = requestJSON(t, client, http.MethodGet, server.URL+"/api/positions", nil)
	if response.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("positions after close = %d %s", response.StatusCode, body)
	}
	_, body = requestJSON(t, client, http.MethodGet, server.URL+"/api/risk", nil)
	var riskSnapshot struct {
		OrdersLastHour int    `json:"orders_last_hour"`
		RealizedPNL    string `json:"realized_pnl"`
	}
	if err := json.Unmarshal(body, &riskSnapshot); err != nil {
		t.Fatalf("decode risk snapshot after close: %v: %s", err, body)
	}
	if riskSnapshot.OrdersLastHour != 2 || !strings.HasPrefix(riskSnapshot.RealizedPNL, "-") {
		t.Fatalf("risk state did not record closed trade: %s", body)
	}
	_, body = requestJSON(t, client, http.MethodGet, server.URL+"/api/trades", nil)
	var trades []map[string]any
	if err := json.Unmarshal(body, &trades); err != nil || len(trades) != 2 {
		t.Fatalf("trade ledger response is incomplete: %v %s", err, body)
	}
	if trades[0]["kind"] != "open" || trades[1]["kind"] != "close" {
		t.Fatalf("trade ledger ordering is invalid: %s", body)
	}
	lessons, err := application.Repository.GetLessons(context.Background(), 20, "BTC/USDT")
	if err != nil || !strings.Contains(strings.Join(lessons, " "), "平仓实现") {
		t.Fatalf("close review lessons were not persisted: %v %#v", err, lessons)
	}
}

func TestSSEUsesDefaultDataFrames(t *testing.T) {
	application, server := newTestApplication(t, func(settings *config.Settings) {
		settings.Automation.Enabled = false
	})
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatalf("GET events error = %v", err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q", got)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "retry: 3000" {
		t.Fatalf("first SSE line = %q, err = %v", line, err)
	}
	application.Events.Emit("killswitch", "-", map[string]any{"on": true})
	data, err := api.ReadSSEData(reader)
	if err != nil {
		t.Fatalf("ReadSSEData() error = %v", err)
	}
	if !strings.Contains(string(data), `"type":"killswitch"`) || !strings.Contains(string(data), `"run_id":"-"`) {
		t.Fatalf("unexpected SSE data: %s", data)
	}
}
