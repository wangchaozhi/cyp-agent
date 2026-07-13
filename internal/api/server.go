// Package api exposes the Go runtime through the existing REST and SSE
// contracts consumed by the React dashboard.
package api

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/approval"
	backtestengine "github.com/wangchaozhi/cyp-agent/internal/backtest"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/control"
	"github.com/wangchaozhi/cyp-agent/internal/data"
	"github.com/wangchaozhi/cyp-agent/internal/events"
	"github.com/wangchaozhi/cyp-agent/internal/llm"
	"github.com/wangchaozhi/cyp-agent/internal/metrics"
	"github.com/wangchaozhi/cyp-agent/internal/observability"
	"github.com/wangchaozhi/cyp-agent/internal/orchestrator"
	"github.com/wangchaozhi/cyp-agent/internal/riskstate"
	runtimecore "github.com/wangchaozhi/cyp-agent/internal/runtime"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

const maxRequestBody = 1 << 20

type Server struct {
	control         *control.State
	venue           venue.Venue
	events          *events.Bus
	gate            *approval.PendingGate
	orchestrator    *orchestrator.Service
	metrics         *metrics.Runs
	runtimeMetrics  *observability.RuntimeMetrics
	registry        *venue.VenueRegistry
	marketData      *data.MarketAggregator
	safety          *runtimecore.SafetyState
	riskState       *riskstate.Tracker
	historicalVenue venue.Venue
	webDir          string
	logger          *slog.Logger
	authToken       string
	corsOrigins     map[string]struct{}
	handler         http.Handler
}

type Dependencies struct {
	Control         *control.State
	Venue           venue.Venue
	Events          *events.Bus
	Gate            *approval.PendingGate
	Orchestrator    *orchestrator.Service
	Metrics         *metrics.Runs
	RuntimeMetrics  *observability.RuntimeMetrics
	Registry        *venue.VenueRegistry
	Market          *data.MarketAggregator
	Safety          *runtimecore.SafetyState
	RiskState       *riskstate.Tracker
	HistoricalVenue venue.Venue
	WebDir          string
	Logger          *slog.Logger
	APIToken        string
}

func New(dependencies Dependencies) (*Server, error) {
	if dependencies.Control == nil || dependencies.Venue == nil || dependencies.Events == nil ||
		dependencies.Gate == nil || dependencies.Orchestrator == nil || dependencies.Metrics == nil {
		return nil, errors.New("api dependencies must not be nil")
	}
	logger := dependencies.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	server := &Server{
		control: dependencies.Control, venue: dependencies.Venue, events: dependencies.Events,
		gate: dependencies.Gate, orchestrator: dependencies.Orchestrator,
		metrics: dependencies.Metrics, runtimeMetrics: dependencies.RuntimeMetrics,
		registry: dependencies.Registry, marketData: dependencies.Market, safety: dependencies.Safety,
		riskState:       dependencies.RiskState,
		historicalVenue: dependencies.HistoricalVenue,
		webDir:          dependencies.WebDir, logger: logger, authToken: strings.TrimSpace(dependencies.APIToken),
		corsOrigins: configuredCORSOrigins(),
	}
	server.handler = server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/ready", s.ready)
	mux.HandleFunc("GET /api/venues", s.venues)
	mux.HandleFunc("GET /api/settings", s.settings)
	mux.HandleFunc("POST /api/settings", s.updateSettings)
	mux.HandleFunc("GET /api/market", s.market)
	mux.HandleFunc("GET /api/market/history", s.marketHistory)
	mux.HandleFunc("GET /api/positions", s.positions)
	mux.HandleFunc("GET /api/trades", s.trades)
	mux.HandleFunc("POST /api/positions/close", s.closePosition)
	mux.HandleFunc("GET /api/metrics", s.metricsSnapshot)
	mux.HandleFunc("GET /api/risk", s.riskSnapshot)
	mux.HandleFunc("GET /api/pending", s.pending)
	mux.HandleFunc("GET /api/portfolio", s.portfolioSnapshot)
	mux.HandleFunc("POST /api/backtest", s.backtest)
	mux.HandleFunc("POST /api/run", s.run)
	mux.HandleFunc("GET /api/runs/{run_id}", s.runStatus)
	mux.HandleFunc("POST /api/approvals/{run_id}", s.approve)
	mux.HandleFunc("GET /api/killswitch", s.killSwitchGet)
	mux.HandleFunc("POST /api/killswitch", s.killSwitchSet)
	mux.HandleFunc("GET /api/events", s.eventStream)
	mux.HandleFunc("GET /assets/", s.assets)
	mux.HandleFunc("GET /", s.index)
	return s.middleware(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	settings := s.control.Settings()
	displayMode := settings.Mode
	if settings.ExecutionVenue == "okx" && settings.OKXDemo {
		displayMode = "OKX Demo"
	} else if settings.ExecutionVenue != "paper" {
		displayMode = strings.ToUpper(settings.ExecutionVenue)
	}
	writeJSON(w, http.StatusOK, contracts.HealthStatus{
		OK: true, Mode: settings.Mode, DisplayMode: displayMode,
		ExecutionVenue: settings.ExecutionVenue, LLM: settings.LLMEnabled(), Kill: settings.Kill,
	})
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	settings := s.control.Settings()
	guard := settings.LiveGuard()
	ready := settings.Mode == "paper" && settings.ExecutionVenue == "paper"
	safety := runtimecore.SafetySnapshot{Frozen: false}
	if s.safety != nil {
		safety = s.safety.Snapshot()
		ready = ready && !safety.Frozen
	}
	reasons := append([]string{}, guard.Reasons...)
	if settings.Mode == "paper" && settings.ExecutionVenue != "paper" {
		reasons = append(reasons, "Go 首版不执行非 Paper 场所")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "ready": ready, "execution_ready": ready && !settings.Kill,
		"reconciling": safety.ReconcileActive, "safety": safety, "reasons": reasons,
	})
}

func (s *Server) venues(w http.ResponseWriter, _ *http.Request) {
	if s.registry != nil {
		writeJSON(w, http.StatusOK, s.registry.Describe())
		return
	}
	settings := s.control.Settings()
	writeJSON(w, http.StatusOK, []contracts.VenueInfo{
		{ID: "paper", Kind: "paper", Configured: true, Spot: true, Perp: true, NativeProtectiveOrders: true, ReadOnly: false},
		{ID: "binance", Kind: "cex", Configured: settings.CEXID == "binance" && settings.CEXTradingConfigured(), Spot: true, Perp: true, NativeProtectiveOrders: true, ReadOnly: true},
		{ID: "okx", Kind: "cex", Configured: settings.OKXConfigured(), Spot: true, Perp: true, NativeProtectiveOrders: true, ReadOnly: true},
	})
}

func (s *Server) settings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.control.Snapshot())
}

func (s *Server) updateSettings(w http.ResponseWriter, request *http.Request) {
	var payload contracts.SettingsUpdateRequest
	if err := decodeJSON(request, &payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := s.control.UpdateSettings(payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.orchestrator.SetLLM(llm.FromSettings(s.control.Settings()))
	writeJSON(w, http.StatusOK, s.control.Snapshot())
}

type marketResponse struct {
	Symbol       string                       `json:"symbol"`
	Tickers      map[string]contracts.Decimal `json:"tickers"`
	BestBuy      marketSide                   `json:"best_buy"`
	BestSell     marketSide                   `json:"best_sell"`
	SpreadBPS    *contracts.Decimal           `json:"spread_bps"`
	FundingRates map[string]contracts.Decimal `json:"funding_rates"`
	ArbHints     []string                     `json:"arb_hints"`
}

type marketSide struct {
	Venue *string            `json:"venue"`
	Price *contracts.Decimal `json:"price"`
}

func (s *Server) market(w http.ResponseWriter, request *http.Request) {
	symbol := strings.TrimSpace(request.URL.Query().Get("symbol"))
	if symbol == "" {
		symbol = defaultSymbol(s.control.Settings().WatchlistSymbols())
	}
	response := marketResponse{
		Symbol: symbol, Tickers: map[string]contracts.Decimal{},
		BestBuy: marketSide{}, BestSell: marketSide{},
		FundingRates: map[string]contracts.Decimal{}, ArbHints: []string{},
	}
	if s.marketData != nil {
		summary := s.marketData.Summary(request.Context(), symbol)
		writeJSON(w, http.StatusOK, summary)
		return
	}
	if price, err := s.venue.FetchTicker(request.Context(), symbol); err == nil {
		venueID := s.venue.ID()
		response.Tickers[venueID] = price
		response.BestBuy = marketSide{Venue: &venueID, Price: &price}
		response.BestSell = marketSide{Venue: &venueID, Price: &price}
		zero := contracts.Zero()
		response.SpreadBPS = &zero
	}
	writeJSON(w, http.StatusOK, response)
}

type marketHistoryPoint struct {
	TS    time.Time         `json:"ts"`
	Close contracts.Decimal `json:"close"`
}

type marketHistorySeries struct {
	Symbol string               `json:"symbol"`
	Points []marketHistoryPoint `json:"points"`
}

type marketHistoryResponse struct {
	Venue     string                `json:"venue"`
	Timeframe string                `json:"timeframe"`
	Series    []marketHistorySeries `json:"series"`
}

const maxMarketHistorySymbols = 6

// marketHistory returns compact close-price series for the dashboard. Upstream
// failures are isolated per symbol so one unavailable market does not hide the
// other selected curves.
func (s *Server) marketHistory(w http.ResponseWriter, request *http.Request) {
	symbols, err := requestedMarketSymbols(request, s.control.Settings().WatchlistSymbols())
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	timeframe := strings.TrimSpace(request.URL.Query().Get("timeframe"))
	if timeframe == "" {
		timeframe = "1h"
	}
	if !validMarketTimeframe(timeframe) {
		writeError(w, http.StatusUnprocessableEntity, "unsupported market history timeframe")
		return
	}
	limit := 48
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 12 || limit > 200 {
			writeError(w, http.StatusUnprocessableEntity, "market history limit must be between 12 and 200")
			return
		}
	}

	response := marketHistoryResponse{Timeframe: timeframe, Series: make([]marketHistorySeries, len(symbols))}
	if s.historicalVenue != nil {
		response.Venue = s.historicalVenue.ID()
	}
	var wait sync.WaitGroup
	for index, symbol := range symbols {
		response.Series[index] = marketHistorySeries{Symbol: symbol, Points: []marketHistoryPoint{}}
		if s.historicalVenue == nil {
			continue
		}
		wait.Add(1)
		go func(index int, symbol string) {
			defer wait.Done()
			candles, fetchErr := s.historicalVenue.FetchOHLCV(request.Context(), symbol, timeframe, limit)
			if fetchErr != nil {
				return
			}
			points := make([]marketHistoryPoint, 0, len(candles))
			for _, candle := range candles {
				if candle.Close.IsPositive() {
					points = append(points, marketHistoryPoint{TS: candle.TS, Close: candle.Close})
				}
			}
			response.Series[index].Points = points
		}(index, symbol)
	}
	wait.Wait()
	writeJSON(w, http.StatusOK, response)
}

func requestedMarketSymbols(request *http.Request, defaults []string) ([]string, error) {
	values := request.URL.Query()["symbol"]
	if len(values) == 0 {
		values = defaults
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			symbol := strings.ToUpper(strings.TrimSpace(item))
			if symbol == "" {
				continue
			}
			if len(symbol) > 48 || !strings.Contains(symbol, "/") {
				return nil, errors.New("invalid market history symbol")
			}
			if _, ok := seen[symbol]; ok {
				continue
			}
			seen[symbol] = struct{}{}
			result = append(result, symbol)
			if len(result) > maxMarketHistorySymbols {
				return nil, fmt.Errorf("market history supports at most %d symbols", maxMarketHistorySymbols)
			}
		}
	}
	if len(result) == 0 {
		result = append(result, defaultSymbol(defaults))
	}
	return result, nil
}

func validMarketTimeframe(value string) bool {
	switch value {
	case "15m", "1h", "4h", "1d":
		return true
	default:
		return false
	}
}

func (s *Server) pending(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.gate.ListPending())
}

func (s *Server) run(w http.ResponseWriter, request *http.Request) {
	var payload contracts.RunRequest
	if err := decodeJSON(request, &payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	symbol := defaultSymbol(s.control.Settings().WatchlistSymbols())
	if payload.Symbol != nil && strings.TrimSpace(*payload.Symbol) != "" {
		symbol = strings.TrimSpace(*payload.Symbol)
	}
	accepted, err := s.orchestrator.Start(symbol)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, accepted)
}

func (s *Server) runStatus(w http.ResponseWriter, request *http.Request) {
	runID := request.PathValue("run_id")
	result, ok := s.orchestrator.GetRun(runID)
	if !ok {
		writeError(w, http.StatusNotFound, "无此 run")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) approve(w http.ResponseWriter, request *http.Request) {
	var payload contracts.ApprovalRequest
	if err := decodeJSON(request, &payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	err := s.gate.Resolve(request.PathValue("run_id"), payload)
	if errors.Is(err, approval.ErrNotPending) {
		writeError(w, http.StatusNotFound, "无此待审批项或已处理")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, contracts.OKResponse{OK: true})
}

func (s *Server) killSwitchGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, contracts.KillStatus{Kill: s.control.Kill()})
}

func (s *Server) killSwitchSet(w http.ResponseWriter, request *http.Request) {
	var payload contracts.KillRequest
	if err := decodeJSON(request, &payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	on := s.control.SetKill(payload.On)
	s.events.Emit("killswitch", "-", map[string]any{"on": on})
	writeJSON(w, http.StatusOK, contracts.KillStatus{Kill: on})
}

func (s *Server) backtest(w http.ResponseWriter, request *http.Request) {
	payload := contracts.BacktestRequest{
		Bars: 300, Window: 60, Seed: 7, Drift: 0.001, Vol: 0.01,
		Data: "synthetic", Timeframe: "1h", FeeRate: 0.0004,
		SlippageBPS: 5, SpreadBPS: 2, FundingRate: 0,
	}
	if err := decodeJSON(request, &payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	symbol := defaultSymbol(s.control.Settings().WatchlistSymbols())
	if payload.Symbol != nil && strings.TrimSpace(*payload.Symbol) != "" {
		symbol = strings.TrimSpace(*payload.Symbol)
	}
	params := backtestengine.Params{
		Symbol: symbol, Bars: payload.Bars, Window: payload.Window, Seed: int64(payload.Seed),
		Drift: payload.Drift, Vol: payload.Vol, Data: payload.Data, Timeframe: payload.Timeframe,
		FeeRate: payload.FeeRate, SlippageBPS: payload.SlippageBPS,
		SpreadBPS: payload.SpreadBPS, FundingRate: payload.FundingRate,
	}
	if err := params.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if params.Data == "cex" {
		if s.historicalVenue == nil {
			writeError(w, http.StatusBadGateway, "真实历史拉取失败：未配置 CEX 行情场所")
			return
		}
		candles, fetchErr := s.historicalVenue.FetchOHLCV(request.Context(), symbol, params.Timeframe, params.Bars)
		if fetchErr != nil {
			writeError(w, http.StatusBadGateway, "真实历史拉取失败："+fetchErr.Error())
			return
		}
		if len(candles) <= params.Window {
			writeError(w, http.StatusBadGateway,
				fmt.Sprintf("真实历史不足：%d 根（需要 > window=%d）", len(candles), params.Window))
			return
		}
		report, runErr := backtestengine.RunCandles(params, candles, backtestengine.DefaultStrategyConfig())
		if runErr != nil {
			writeError(w, http.StatusBadGateway, "真实历史回测失败："+runErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, report)
		return
	}
	report, err := backtestengine.Run(params)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) eventStream(w http.ResponseWriter, request *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	subscription := s.events.Subscribe(1000)
	defer subscription.Cancel()
	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "retry: 3000\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case event, open := <-subscription.C:
			if !open {
				return
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				s.logger.Error("sse_encode_failed", "error", err)
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		case <-request.Context().Done():
			return
		}
	}
}

func (s *Server) assets(w http.ResponseWriter, request *http.Request) {
	if strings.TrimSpace(s.webDir) == "" {
		http.NotFound(w, request)
		return
	}
	assetsDir := filepath.Join(s.webDir, "assets")
	if info, err := os.Stat(assetsDir); err != nil || !info.IsDir() {
		http.NotFound(w, request)
		return
	}
	http.StripPrefix("/assets/", http.FileServer(http.Dir(assetsDir))).ServeHTTP(w, request)
}

func (s *Server) index(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(w, request)
		return
	}
	if s.webDir != "" {
		index := filepath.Join(s.webDir, "index.html")
		if info, err := os.Stat(index); err == nil && !info.IsDir() {
			http.ServeFile(w, request, index)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "<h1>cyp-agent</h1><p>Go 后端已启动，React 仪表盘尚未构建。</p>"+
		"<p>运行：<code>cd apps/web &amp;&amp; npm install &amp;&amp; npm run build</code></p>")
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		started := time.Now()
		requestID := strings.TrimSpace(request.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = shortID()
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if origin := strings.TrimSpace(request.Header.Get("Origin")); origin != "" {
			if _, allowed := s.corsOrigins[origin]; allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-CYP-API-Token, X-Request-ID")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.Header().Add("Vary", "Origin")
				if request.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("http_panic", "request_id", requestID, "panic", fmt.Sprint(recovered))
				writeError(w, http.StatusInternalServerError, "内部错误")
			}
			s.logger.Info("http_request", "request_id", requestID, "method", request.Method,
				"path", request.URL.Path, "duration_ms", time.Since(started).Milliseconds())
		}()
		if isMutation(request.Method) {
			if !s.sameOriginOrNonBrowser(request) {
				writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
				return
			}
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(request.Header.Get("Content-Type"))), "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
				return
			}
			if !s.authorized(request) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeError(w, http.StatusUnauthorized, "valid CYP API token required")
				return
			}
		}
		next.ServeHTTP(w, request)
	})
}

func isMutation(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func (s *Server) sameOriginOrNonBrowser(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	if strings.EqualFold(parsed.Host, request.Host) {
		return true
	}
	_, allowed := s.corsOrigins[origin]
	return allowed
}

func configuredCORSOrigins() map[string]struct{} {
	origins := make(map[string]struct{})
	value := strings.TrimSpace(os.Getenv("CYP_CORS_ORIGINS"))
	if value == "" {
		value = "http://127.0.0.1:5173,http://localhost:5173"
	}
	for _, origin := range strings.Split(value, ",") {
		if origin = strings.TrimRight(strings.TrimSpace(origin), "/"); origin != "" {
			origins[origin] = struct{}{}
		}
	}
	return origins
}

func (s *Server) authorized(request *http.Request) bool {
	if s.authToken == "" {
		return true
	}
	candidate := strings.TrimSpace(request.Header.Get("X-CYP-API-Token"))
	if authorization := strings.TrimSpace(request.Header.Get("Authorization")); candidate == "" && len(authorization) > 7 && strings.EqualFold(authorization[:7], "Bearer ") {
		candidate = strings.TrimSpace(authorization[7:])
	}
	want := sha256.Sum256([]byte(s.authToken))
	got := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

func decodeJSON(request *http.Request, target any) error {
	if request.Body == nil {
		return errors.New("request body is required")
	}
	defer request.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(nil, request.Body, maxRequestBody))
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is required")
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		return
	}
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}

func defaultSymbol(symbols []string) string {
	if len(symbols) == 0 || strings.TrimSpace(symbols[0]) == "" {
		return "BTC/USDT"
	}
	return strings.TrimSpace(symbols[0])
}

func shortID() string {
	var value [6]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

// ReadSSEData is used by contract tests and operational smoke tools.
func ReadSSEData(reader *bufio.Reader) ([]byte, error) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, "data: ") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), nil
		}
	}
}
