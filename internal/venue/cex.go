package venue

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

const maxCEXResponseBytes = 4 << 20

type CEXConfig struct {
	ExchangeID           string
	BaseURL              string
	FuturesBaseURL       string
	HTTPClient           *http.Client
	APIKey               string
	APISecret            string
	Passphrase           string
	Demo                 bool
	EnableDemoTrading    bool
	QuoteCurrency        string
	EstimatedSlippageBPS contracts.Decimal
	Clock                func() time.Time
}

type MarketPrecision struct {
	PriceStep   contracts.Decimal
	AmountStep  contracts.Decimal
	MinNotional contracts.Decimal
}

// CEXVenue exposes public and signed exchange APIs. Order mutation is enabled
// only for an explicitly selected, fully configured OKX Demo account. Real CEX
// trading remains unavailable even when production credentials are present.
type CEXVenue struct {
	id             string
	baseURL        string
	futuresBaseURL string
	httpClient     *http.Client
	apiKey         string
	apiSecret      string
	passphrase     string
	demo           bool
	demoTrading    bool
	quoteCurrency  string
	slippageBPS    contracts.Decimal
	now            func() time.Time

	placeMu     sync.Mutex
	mu          sync.RWMutex
	precision   map[string]MarketPrecision
	instruments map[string]okxInstrumentSpec
	fills       map[string]contracts.ExecutionResult
	orderRefs   map[string]okxOrderRef
}

var _ Venue = (*CEXVenue)(nil)

func NewCEXVenue(config CEXConfig) (*CEXVenue, error) {
	exchange := strings.ToLower(strings.TrimSpace(config.ExchangeID))
	if exchange == "" {
		exchange = "binance"
	}
	if exchange != "binance" && exchange != "okx" {
		return nil, &CEXError{Kind: CEXErrorUnsupported, Exchange: exchange, Operation: "construct", Message: "unsupported exchange"}
	}
	baseURLWasProvided := config.BaseURL != ""
	if !baseURLWasProvided {
		if exchange == "okx" {
			config.BaseURL = "https://www.okx.com"
		} else {
			config.BaseURL = "https://api.binance.com"
		}
	}
	if config.FuturesBaseURL == "" {
		if exchange == "binance" && !baseURLWasProvided {
			config.FuturesBaseURL = "https://fapi.binance.com"
		} else {
			config.FuturesBaseURL = config.BaseURL
		}
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, &CEXError{Kind: CEXErrorValidation, Exchange: exchange, Operation: "construct", Message: "invalid base URL", Err: err}
	}
	futuresParsed, futuresErr := url.Parse(config.FuturesBaseURL)
	if futuresErr != nil || futuresParsed.Scheme == "" || futuresParsed.Host == "" {
		return nil, &CEXError{Kind: CEXErrorValidation, Exchange: exchange, Operation: "construct", Message: "invalid futures base URL", Err: futuresErr}
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if config.QuoteCurrency == "" {
		config.QuoteCurrency = "USDT"
	}
	if config.EstimatedSlippageBPS.IsZero() {
		config.EstimatedSlippageBPS = contracts.MustDecimal("10")
	}
	if config.EstimatedSlippageBPS.IsNegative() {
		return nil, &CEXError{Kind: CEXErrorValidation, Exchange: exchange, Operation: "construct", Message: "negative slippage"}
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.EnableDemoTrading {
		if exchange != "okx" || !config.Demo {
			return nil, &CEXError{
				Kind: CEXErrorValidation, Exchange: exchange, Operation: "construct",
				Message: "CEX order execution is allowed only for OKX Demo",
			}
		}
		if strings.TrimSpace(config.APIKey) == "" || strings.TrimSpace(config.APISecret) == "" || strings.TrimSpace(config.Passphrase) == "" {
			return nil, &CEXError{
				Kind: CEXErrorAuth, Exchange: exchange, Operation: "construct",
				Message: "OKX Demo trading requires API key, secret, and passphrase",
			}
		}
	}
	return &CEXVenue{
		id: exchange, baseURL: strings.TrimRight(config.BaseURL, "/"),
		futuresBaseURL: strings.TrimRight(config.FuturesBaseURL, "/"), httpClient: config.HTTPClient,
		apiKey: config.APIKey, apiSecret: config.APISecret, passphrase: config.Passphrase,
		demo: config.Demo, demoTrading: config.EnableDemoTrading, quoteCurrency: config.QuoteCurrency,
		slippageBPS: config.EstimatedSlippageBPS, now: config.Clock,
		precision: make(map[string]MarketPrecision), instruments: make(map[string]okxInstrumentSpec),
		fills: make(map[string]contracts.ExecutionResult), orderRefs: make(map[string]okxOrderRef),
	}, nil
}

func NewBinanceVenue(config CEXConfig) (*CEXVenue, error) {
	config.ExchangeID = "binance"
	return NewCEXVenue(config)
}

func NewOKXVenue(config CEXConfig) (*CEXVenue, error) {
	config.ExchangeID = "okx"
	return NewCEXVenue(config)
}

func (venue *CEXVenue) ID() string { return venue.id }
func (*CEXVenue) Kind() Kind       { return KindCEX }
func (venue *CEXVenue) ExecutionIdentity() ExecutionIdentity {
	environment := EnvironmentLive
	if venue.Demo() {
		environment = EnvironmentDemo
	}
	return ExecutionIdentity{
		VenueID: venue.ID(), Kind: venue.Kind(), Environment: environment,
		Writable: venue.DemoTradingEnabled(),
	}
}
func (venue *CEXVenue) Caps() Caps {
	return Caps{Spot: true, Perp: true, NativeProtectiveOrders: true, ReadOnly: !venue.DemoTradingEnabled()}
}

// IsConfigured reports public/read-only availability. PrivateConfigured is the
// stricter credential check for signed account reads.
func (*CEXVenue) IsConfigured() bool { return true }

func (venue *CEXVenue) PrivateConfigured() bool {
	if venue.apiKey == "" || venue.apiSecret == "" {
		return false
	}
	return venue.id != "okx" || venue.passphrase != ""
}

func (venue *CEXVenue) Demo() bool { return venue.demo }

// DemoTradingEnabled is deliberately narrower than Demo: it proves that the
// adapter was constructed for order mutation, points at OKX's simulated
// environment, and has the complete private credential tuple.
func (venue *CEXVenue) DemoTradingEnabled() bool {
	return venue != nil && venue.id == "okx" && venue.demo && venue.demoTrading && venue.PrivateConfigured()
}

func (venue *CEXVenue) String() string {
	return fmt.Sprintf("CEXVenue(id=%s,demo=%t,private_configured=%t,demo_trading=%t)",
		venue.id, venue.demo, venue.PrivateConfigured(), venue.DemoTradingEnabled())
}

func (venue *CEXVenue) GoString() string { return venue.String() }

func (venue *CEXVenue) SetMarketPrecision(symbol string, precision MarketPrecision) error {
	if precision.PriceStep.IsNegative() || precision.AmountStep.IsNegative() || precision.MinNotional.IsNegative() {
		return &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "precision", Message: "precision values cannot be negative"}
	}
	venue.mu.Lock()
	venue.precision[symbol] = precision
	venue.mu.Unlock()
	return nil
}

func (venue *CEXVenue) MarketPrecision(symbol string) (MarketPrecision, bool) {
	venue.mu.RLock()
	defer venue.mu.RUnlock()
	value, ok := venue.precision[symbol]
	return value, ok
}

func QuantizeDown(value, increment contracts.Decimal) (contracts.Decimal, error) {
	if !increment.IsPositive() {
		return contracts.Zero(), errors.New("precision increment must be positive")
	}
	units, err := value.QuoScale(increment, 0, contracts.RoundDown)
	if err != nil {
		return contracts.Zero(), err
	}
	return units.Mul(increment), nil
}

func (venue *CEXVenue) Preflight(ctx context.Context, intent contracts.OrderIntent) (PreflightReport, error) {
	last, err := venue.FetchTicker(ctx, intent.Symbol)
	if err != nil {
		return PreflightReport{OK: false, EstPrice: contracts.Zero(), Reasons: contracts.List[string]{"行情不可用:" + err.Error()}}, nil
	}
	slip, err := venue.slippageBPS.Quo(contracts.NewDecimalFromInt64(10_000))
	if err != nil {
		return PreflightReport{}, err
	}
	up := intent.Side == contracts.SideLong && !intent.ReduceOnly
	estimated := last.Mul(contracts.NewDecimalFromInt64(1).Sub(slip))
	if up {
		estimated = last.Mul(contracts.NewDecimalFromInt64(1).Add(slip))
	}
	report := PreflightReport{OK: true, EstPrice: estimated, EstSlippageBPS: decimalPointer(venue.slippageBPS)}
	if intent.Instrument == contracts.InstrumentPerp && intent.Leverage > 0 {
		leverage, parseErr := contracts.ParseDecimal(strconv.FormatFloat(intent.Leverage, 'g', -1, 64))
		if parseErr != nil || !leverage.IsPositive() {
			report.OK = false
			report.Reasons = contracts.List[string]{"无效杠杆"}
			return report, nil
		}
		inverse, _ := contracts.NewDecimalFromInt64(1).Quo(leverage)
		liquidation := estimated.Mul(contracts.NewDecimalFromInt64(1).Add(inverse))
		if intent.Side == contracts.SideLong {
			liquidation = estimated.Mul(contracts.NewDecimalFromInt64(1).Sub(inverse))
		}
		report.EstLiquidationPrice = decimalPointer(liquidation)
	}
	if precision, ok := venue.MarketPrecision(intent.Symbol); ok &&
		precision.MinNotional.IsPositive() && intent.SizeQuote.Cmp(precision.MinNotional) < 0 {
		report.OK = false
		report.Reasons = append(report.Reasons, "低于交易所最小名义金额")
	}
	return report, nil
}

func (venue *CEXVenue) Place(ctx context.Context, intent contracts.OrderIntent) (contracts.ExecutionResult, error) {
	if strings.TrimSpace(intent.ClientID) == "" {
		return contracts.ExecutionResult{}, ErrClientIDRequired
	}
	venue.placeMu.Lock()
	defer venue.placeMu.Unlock()
	venue.mu.RLock()
	existing, ok := venue.fills[intent.ClientID]
	venue.mu.RUnlock()
	if ok {
		return cloneExecution(existing), nil
	}
	if !venue.DemoTradingEnabled() {
		message := ErrCEXTradingDisabled.Error()
		result := contracts.ExecutionResult{
			ClientID: intent.ClientID, Status: contracts.OrderStatusRejected,
			ProtectiveOrders: contracts.List[contracts.ProtectiveOrder]{}, Error: stringPointer(message),
		}
		venue.rememberCEXResult(intent.ClientID, result, okxOrderRef{})
		return cloneExecution(result), nil
	}
	result, reference, err := venue.placeOKXDemo(ctx, intent)
	if err != nil {
		return contracts.ExecutionResult{}, err
	}
	venue.rememberCEXResult(intent.ClientID, result, reference)
	return cloneExecution(result), nil
}

func (venue *CEXVenue) Cancel(ctx context.Context, clientID string) error {
	if !venue.DemoTradingEnabled() {
		return &CEXError{
			Kind: CEXErrorDisabled, Exchange: venue.id, Operation: "cancel",
			Message: ErrCEXTradingDisabled.Error(), Err: ErrCEXTradingDisabled,
		}
	}
	venue.mu.RLock()
	reference, ok := venue.orderRefs[clientID]
	venue.mu.RUnlock()
	if !ok || reference.OrderID == "" || reference.InstrumentID == "" {
		return &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "cancel", Message: "unknown client order id"}
	}
	return venue.cancelOKXDemo(ctx, reference)
}

func (venue *CEXVenue) rememberCEXResult(clientID string, result contracts.ExecutionResult, reference okxOrderRef) {
	venue.mu.Lock()
	venue.fills[clientID] = cloneExecution(result)
	if reference.OrderID != "" {
		venue.orderRefs[clientID] = reference
	}
	venue.mu.Unlock()
}

func (venue *CEXVenue) Close() error {
	venue.httpClient.CloseIdleConnections()
	return nil
}

func (venue *CEXVenue) EntryParameters(intent contracts.OrderIntent, isClose bool) map[string]any {
	if venue.id == "okx" {
		tdMode := "cash"
		if intent.Instrument == contracts.InstrumentPerp {
			tdMode = string(intent.MarginMode)
		}
		params := map[string]any{"clientOrderId": SanitizeOKXClientID(intent.ClientID, ""), "tdMode": tdMode}
		if intent.Instrument == contracts.InstrumentPerp && isClose {
			params["reduceOnly"] = true
		}
		return params
	}
	params := map[string]any{"clientOrderId": intent.ClientID}
	if intent.Instrument == contracts.InstrumentPerp {
		params["reduceOnly"] = isClose
	}
	return params
}

var nonAlphanumeric = regexp.MustCompile(`[^A-Za-z0-9]`)

func SanitizeOKXClientID(raw, suffix string) string {
	value := nonAlphanumeric.ReplaceAllString(raw+suffix, "")
	if value == "" {
		value = "c"
	}
	if len(value) > 32 {
		value = value[:32]
	}
	return value
}

// NewHTTPRequest is exported for adapter-level testing and future read-only
// endpoints. private requests are signed without exposing secret material.
func (venue *CEXVenue) NewHTTPRequest(
	ctx context.Context,
	method, path string,
	query url.Values,
	body []byte,
	private bool,
) (*http.Request, error) {
	return venue.newHTTPRequestAt(ctx, venue.baseURL, method, path, query, body, private)
}

// NewFuturesHTTPRequest is the signed/read-only request builder for Binance's
// distinct USD-M futures host. OKX uses the same host for both surfaces.
func (venue *CEXVenue) NewFuturesHTTPRequest(
	ctx context.Context,
	method, path string,
	query url.Values,
	body []byte,
	private bool,
) (*http.Request, error) {
	return venue.newHTTPRequestAt(ctx, venue.futuresBaseURL, method, path, query, body, private)
}

func (venue *CEXVenue) newHTTPRequestAt(
	ctx context.Context,
	baseURL, method, path string,
	query url.Values,
	body []byte,
	private bool,
) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "://") || strings.ContainsAny(path, "\r\n") {
		return nil, &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "request", Message: "invalid request path"}
	}
	if query == nil {
		query = make(url.Values)
	} else {
		query = cloneValues(query)
	}
	if private && !venue.PrivateConfigured() {
		return nil, &CEXError{Kind: CEXErrorAuth, Exchange: venue.id, Operation: path, Message: "private API credentials are incomplete"}
	}
	if private && venue.id == "binance" {
		query.Set("timestamp", strconv.FormatInt(venue.now().UTC().UnixMilli(), 10))
		signature := hmacHex(venue.apiSecret, query.Encode())
		query.Set("signature", signature)
	}
	requestURL := baseURL + path
	if encoded := query.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if private && venue.id == "binance" {
		request.Header.Set("X-MBX-APIKEY", venue.apiKey)
	}
	if private && venue.id == "okx" {
		timestamp := venue.now().UTC().Format("2006-01-02T15:04:05.000Z")
		requestPath := path
		if encoded := query.Encode(); encoded != "" {
			requestPath += "?" + encoded
		}
		prehash := timestamp + strings.ToUpper(method) + requestPath + string(body)
		request.Header.Set("OK-ACCESS-KEY", venue.apiKey)
		request.Header.Set("OK-ACCESS-SIGN", hmacBase64(venue.apiSecret, prehash))
		request.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
		request.Header.Set("OK-ACCESS-PASSPHRASE", venue.passphrase)
		if venue.demo {
			request.Header.Set("x-simulated-trading", "1")
		}
	}
	return request, nil
}

func (venue *CEXVenue) doJSON(
	ctx context.Context,
	method, path string,
	query url.Values,
	private bool,
	target any,
) error {
	return venue.doJSONAt(ctx, venue.baseURL, method, path, query, private, target)
}

func (venue *CEXVenue) doJSONAt(
	ctx context.Context,
	baseURL, method, path string,
	query url.Values,
	private bool,
	target any,
) error {
	return venue.doJSONBodyAt(ctx, baseURL, method, path, query, nil, private, target)
}

func (venue *CEXVenue) doJSONBody(
	ctx context.Context,
	method, path string,
	query url.Values,
	body []byte,
	private bool,
	target any,
) error {
	return venue.doJSONBodyAt(ctx, venue.baseURL, method, path, query, body, private, target)
}

func (venue *CEXVenue) doJSONBodyAt(
	ctx context.Context,
	baseURL, method, path string,
	query url.Values,
	body []byte,
	private bool,
	target any,
) error {
	request, err := venue.newHTTPRequestAt(ctx, baseURL, method, path, query, body, private)
	if err != nil {
		return err
	}
	response, err := venue.httpClient.Do(request)
	if err != nil {
		return &CEXError{Kind: CEXErrorTemporary, Exchange: venue.id, Operation: path, Message: "HTTP request failed", Err: err}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxCEXResponseBytes+1))
	if err != nil {
		return &CEXError{Kind: CEXErrorTemporary, Exchange: venue.id, Operation: path, Message: "read response failed", Err: err}
	}
	if len(raw) > maxCEXResponseBytes {
		return &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: path, Message: "response exceeds size limit"}
	}
	message, code := exchangeErrorPayload(raw)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		classified := classifyHTTPError(venue.id, path, response, message)
		classified.Code = code
		return classified
	}
	if code != "" && !successfulExchangeCode(venue.id, code) {
		return classifyExchangeCode(venue.id, path, code, message)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: path, Message: "invalid JSON response", Err: err}
	}
	return nil
}

func exchangeErrorPayload(raw []byte) (message, code string) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if decoder.Decode(&payload) != nil {
		return "", ""
	}
	if value, ok := payload["msg"].(string); ok {
		message = value
	}
	if value, ok := payload["message"].(string); ok && message == "" {
		message = value
	}
	if value, ok := payload["code"]; ok {
		code = fmt.Sprint(value)
	}
	return message, code
}

func successfulExchangeCode(exchange, code string) bool {
	if exchange == "okx" {
		return code == "0"
	}
	return code == "0" || code == "200" || !strings.HasPrefix(code, "-")
}

func classifyExchangeCode(exchange, operation, code, message string) *CEXError {
	kind := CEXErrorUpstream
	if exchange == "okx" {
		switch code {
		case "50011", "50040":
			kind = CEXErrorRateLimit
		case "50004", "50026":
			kind = CEXErrorTemporary
		default:
			if strings.HasPrefix(code, "501") {
				kind = CEXErrorAuth
			}
		}
	} else {
		switch code {
		case "-1003":
			kind = CEXErrorRateLimit
		case "-1001", "-1007":
			kind = CEXErrorTemporary
		case "-2014", "-2015":
			kind = CEXErrorAuth
		}
	}
	return &CEXError{Kind: kind, Exchange: exchange, Operation: operation, Code: code, Message: message}
}

func cloneValues(input url.Values) url.Values {
	result := make(url.Values, len(input))
	for key, values := range input {
		result[key] = append([]string(nil), values...)
	}
	return result
}

func hmacHex(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func hmacBase64(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func decimalFromAny(value any) (contracts.Decimal, error) {
	switch typed := value.(type) {
	case string:
		return contracts.ParseDecimal(typed)
	case json.Number:
		return contracts.ParseDecimal(typed.String())
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return contracts.Zero(), errors.New("non-finite decimal")
		}
		return contracts.ParseDecimal(strconv.FormatFloat(typed, 'g', -1, 64))
	case nil:
		return contracts.Zero(), errors.New("missing decimal")
	default:
		return contracts.ParseDecimal(fmt.Sprint(value))
	}
}

func milliseconds(value any) (time.Time, error) {
	text := fmt.Sprint(value)
	if number, ok := value.(json.Number); ok {
		text = number.String()
	}
	stamp, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(stamp).UTC(), nil
}

func sortCandles(candles []contracts.Candle) {
	sort.Slice(candles, func(i, j int) bool { return candles[i].TS.Before(candles[j].TS) })
}
