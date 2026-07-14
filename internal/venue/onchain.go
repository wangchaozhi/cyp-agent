package venue

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

var (
	ErrOnchainNotConfigured   = errors.New("链上场所未配置")
	ErrOnchainTradingDisabled = errors.New("首版 Go 默认硬禁用链上执行")
	ErrRouterNotWhitelisted   = errors.New("DEX router 不在白名单")
	ErrMEVProtectionRequired  = errors.New("DEX 路由未启用 MEV 防护")
)

type SwapQuote struct {
	Price        contracts.Decimal
	PriceImpact  contracts.Decimal
	PoolTVLUSD   contracts.Decimal
	Router       string
	GasQuote     contracts.Decimal
	MEVProtected bool
}

type TransactionReceipt struct {
	Status       uint64
	GasUsedQuote contracts.Decimal
}

// OnchainClient is intentionally injectable. This package contains no default
// RPC/private-key implementation, so execution can only be enabled with an
// explicitly supplied adapter (tests, shadow environments, or a reviewed KMS
// integration).
type OnchainClient interface {
	QuoteSwap(context.Context, string, contracts.Side, contracts.Decimal) (SwapQuote, error)
	Allowance(context.Context, string, string, string) (contracts.Decimal, error)
	PendingNonce(context.Context, string) (uint64, error)
	SendApprove(context.Context, Signer, string, string, contracts.Decimal, uint64) (string, error)
	SendSwap(context.Context, Signer, string, contracts.Side, contracts.Decimal, contracts.Decimal, uint64) (string, error)
	WaitReceipt(context.Context, string) (*TransactionReceipt, error)
}

type OnchainConfig struct {
	Chain               string
	QuoteCurrency       string
	InitialQuote        contracts.Decimal
	RouterWhitelist     []string
	AllowUnprotectedMEV bool
	EnableExecution     bool
	MinOutTolerance     contracts.Decimal
	Signer              Signer
}

type OnchainQuoteContext struct {
	Onchain         bool               `json:"onchain"`
	ApprovalAmount  *contracts.Decimal `json:"approval_amount"`
	ContractAddress *string            `json:"contract_address"`
	PoolTVLUSD      *contracts.Decimal `json:"pool_tvl_usd"`
	EstimatedGas    *contracts.Decimal `json:"est_gas_quote"`
	MEVProtected    bool               `json:"mev_protected"`
}

type OnchainReconcileReport struct {
	Nonce         uint64                 `json:"nonce"`
	Discrepancies contracts.List[string] `json:"discrepancies"`
	Settled       contracts.List[string] `json:"settled"`
	Pending       contracts.List[string] `json:"pending"`
}

type OnchainVenue struct {
	id                  string
	chain               string
	quoteCurrency       string
	client              OnchainClient
	signer              Signer
	allowUnprotectedMEV bool
	executionEnabled    bool
	minOutTolerance     contracts.Decimal
	whitelist           map[string]struct{}

	execMu    sync.Mutex
	mu        sync.RWMutex
	freeQuote contracts.Decimal
	positions map[string]contracts.Position
	fills     map[string]contracts.ExecutionResult
	nonce     *uint64
	pending   map[string]string
}

var _ Venue = (*OnchainVenue)(nil)

// onchainExecutionSupported stays false even though the CEX live gate
// (config.LiveExecutionSupported) has been opened for OKX. Onchain execution
// has its own unfinished acceptance checklist and remains hard-disabled.
const onchainExecutionSupported = false

func NewOnchainVenue(config OnchainConfig, client OnchainClient) (*OnchainVenue, error) {
	return newOnchainVenue(config, client, onchainExecutionSupported)
}

// newOnchainVenue keeps transaction mechanics testable while the exported
// production constructor remains bound to the compile-time live safety rail.
func newOnchainVenue(config OnchainConfig, client OnchainClient, executionSupported bool) (*OnchainVenue, error) {
	chain := strings.ToLower(strings.TrimSpace(config.Chain))
	if chain == "" {
		chain = "ethereum"
	}
	if config.QuoteCurrency == "" {
		config.QuoteCurrency = "USDC"
	}
	if config.InitialQuote.IsNegative() {
		return nil, errors.New("onchain initial quote cannot be negative")
	}
	if config.MinOutTolerance.IsZero() {
		config.MinOutTolerance = contracts.MustDecimal("0.01")
	}
	if config.MinOutTolerance.IsNegative() || config.MinOutTolerance.Cmp(contracts.NewDecimalFromInt64(1)) >= 0 {
		return nil, errors.New("min-out tolerance must be in [0,1)")
	}
	whitelist := make(map[string]struct{}, len(config.RouterWhitelist))
	for _, router := range config.RouterWhitelist {
		router = normalizeAddress(router)
		if router != "" {
			whitelist[router] = struct{}{}
		}
	}
	if config.EnableExecution {
		if !executionSupported {
			return nil, ErrOnchainTradingDisabled
		}
		if client == nil || config.Signer == nil {
			return nil, errors.New("onchain execution requires an injected client and isolated signer")
		}
		if len(whitelist) == 0 {
			return nil, errors.New("onchain execution requires a non-empty router whitelist")
		}
	}
	return &OnchainVenue{
		id: "onchain-" + chain, chain: chain, quoteCurrency: config.QuoteCurrency,
		client: client, signer: config.Signer, allowUnprotectedMEV: config.AllowUnprotectedMEV,
		executionEnabled: config.EnableExecution, minOutTolerance: config.MinOutTolerance,
		whitelist: whitelist, freeQuote: config.InitialQuote,
		positions: make(map[string]contracts.Position), fills: make(map[string]contracts.ExecutionResult),
		pending: make(map[string]string),
	}, nil
}

func (venue *OnchainVenue) ID() string { return venue.id }
func (*OnchainVenue) Kind() Kind       { return KindOnchain }
func (venue *OnchainVenue) Caps() Caps {
	return Caps{Spot: true, Perp: false, NativeProtectiveOrders: false, ReadOnly: !venue.executionEnabled}
}
func (venue *OnchainVenue) IsConfigured() bool     { return venue.client != nil }
func (venue *OnchainVenue) ExecutionEnabled() bool { return venue.executionEnabled }

func (venue *OnchainVenue) String() string {
	return fmt.Sprintf("OnchainVenue(id=%s,configured=%t,execution=%t,signer=%v)",
		venue.id, venue.IsConfigured(), venue.executionEnabled, venue.signer)
}

func (venue *OnchainVenue) GoString() string { return venue.String() }

func (venue *OnchainVenue) FetchTicker(ctx context.Context, symbol string) (contracts.Decimal, error) {
	if venue.client == nil {
		return contracts.Zero(), ErrOnchainNotConfigured
	}
	quote, err := venue.client.QuoteSwap(ctx, symbol, contracts.SideLong, contracts.NewDecimalFromInt64(1))
	if err != nil {
		return contracts.Zero(), err
	}
	if !quote.Price.IsPositive() {
		return contracts.Zero(), errors.New("DEX 无有效报价")
	}
	return quote.Price, nil
}

func (*OnchainVenue) FetchOHLCV(ctx context.Context, _, _ string, _ int) ([]contracts.Candle, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return []contracts.Candle{}, nil
}

func (*OnchainVenue) FetchOrderBook(ctx context.Context, _ string, _ int) (contracts.OrderBook, error) {
	if err := contextError(ctx); err != nil {
		return contracts.OrderBook{}, err
	}
	return contracts.OrderBook{}, nil
}

func (venue *OnchainVenue) Positions(ctx context.Context) ([]contracts.Position, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	venue.mu.RLock()
	defer venue.mu.RUnlock()
	result := make([]contracts.Position, 0, len(venue.positions))
	for _, position := range venue.positions {
		result = append(result, clonePosition(position))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Symbol < result[j].Symbol })
	return result, nil
}

func (venue *OnchainVenue) Balances(ctx context.Context) (contracts.Balances, error) {
	if err := contextError(ctx); err != nil {
		return contracts.Balances{}, err
	}
	venue.mu.RLock()
	defer venue.mu.RUnlock()
	equity := venue.freeQuote
	for _, position := range venue.positions {
		equity = equity.Add(position.SizeBase.Mul(position.EntryPrice))
	}
	return contracts.Balances{QuoteCCY: venue.quoteCurrency, FreeQuote: venue.freeQuote, TotalQuote: equity}, nil
}

func (venue *OnchainVenue) Preflight(
	ctx context.Context,
	intent contracts.OrderIntent,
) (PreflightReport, error) {
	if venue.client == nil {
		return PreflightReport{OK: false, EstPrice: contracts.Zero(), Reasons: contracts.List[string]{ErrOnchainNotConfigured.Error()}}, nil
	}
	quote, err := venue.client.QuoteSwap(ctx, intent.Symbol, intent.Side, intent.SizeQuote)
	if err != nil {
		return PreflightReport{}, err
	}
	return venue.preflightFromQuote(quote), nil
}

func (venue *OnchainVenue) preflightFromQuote(quote SwapQuote) PreflightReport {
	reasons := make(contracts.List[string], 0)
	if !quote.Price.IsPositive() {
		reasons = append(reasons, "DEX 无有效报价")
	}
	if !venue.routerAllowed(quote.Router) {
		reasons = append(reasons, ErrRouterNotWhitelisted.Error())
	}
	if !quote.MEVProtected && !venue.allowUnprotectedMEV {
		reasons = append(reasons, ErrMEVProtectionRequired.Error())
	}
	if quote.PriceImpact.IsNegative() {
		reasons = append(reasons, "DEX 价格冲击无效")
	}
	impactBPS := quote.PriceImpact.Mul(contracts.NewDecimalFromInt64(10_000))
	return PreflightReport{
		OK: len(reasons) == 0, EstPrice: quote.Price,
		EstSlippageBPS: decimalPointer(impactBPS), EstPriceImpact: decimalPointer(quote.PriceImpact),
		Reasons: reasons,
	}
}

func (venue *OnchainVenue) QuoteContext(
	ctx context.Context,
	intent contracts.OrderIntent,
) (OnchainQuoteContext, error) {
	if venue.client == nil {
		return OnchainQuoteContext{}, ErrOnchainNotConfigured
	}
	quote, err := venue.client.QuoteSwap(ctx, intent.Symbol, intent.Side, intent.SizeQuote)
	if err != nil {
		return OnchainQuoteContext{}, err
	}
	allowance, err := venue.client.Allowance(ctx, venue.signerAddress(), intent.Symbol, quote.Router)
	if err != nil {
		return OnchainQuoteContext{}, err
	}
	var approval *contracts.Decimal
	if allowance.Cmp(intent.SizeQuote) < 0 {
		approval = decimalPointer(intent.SizeQuote)
	}
	router := quote.Router
	return OnchainQuoteContext{
		Onchain: true, ApprovalAmount: approval, ContractAddress: &router,
		PoolTVLUSD: decimalPointer(quote.PoolTVLUSD), EstimatedGas: decimalPointer(quote.GasQuote),
		MEVProtected: quote.MEVProtected,
	}, nil
}

func (venue *OnchainVenue) Place(
	ctx context.Context,
	intent contracts.OrderIntent,
) (contracts.ExecutionResult, error) {
	if strings.TrimSpace(intent.ClientID) == "" {
		return contracts.ExecutionResult{}, ErrClientIDRequired
	}
	venue.execMu.Lock()
	defer venue.execMu.Unlock()
	venue.mu.RLock()
	if existing, ok := venue.fills[intent.ClientID]; ok {
		venue.mu.RUnlock()
		return cloneExecution(existing), nil
	}
	venue.mu.RUnlock()
	if !venue.executionEnabled {
		return venue.remember(intent.ClientID, rejectedOnchain(intent.ClientID, venue.chain, ErrOnchainTradingDisabled.Error())), nil
	}
	result, err := venue.placeInner(ctx, intent)
	if err != nil {
		result = failedOnchain(intent.ClientID, venue.chain, "链上执行异常："+err.Error(), "")
	}
	return venue.remember(intent.ClientID, result), nil
}

func (venue *OnchainVenue) placeInner(
	ctx context.Context,
	intent contracts.OrderIntent,
) (contracts.ExecutionResult, error) {
	quote, err := venue.client.QuoteSwap(ctx, intent.Symbol, intent.Side, intent.SizeQuote)
	if err != nil {
		return contracts.ExecutionResult{}, err
	}
	preflight := venue.preflightFromQuote(quote)
	if !preflight.OK {
		return rejectedOnchain(intent.ClientID, venue.chain, strings.Join(preflight.Reasons, "; ")), nil
	}
	if intent.ReduceOnly || intent.Side == contracts.SideFlat {
		return venue.closePosition(ctx, intent, quote)
	}
	if intent.Side != contracts.SideLong || (intent.Instrument != "" && intent.Instrument != contracts.InstrumentSpot) {
		return rejectedOnchain(intent.ClientID, venue.chain, "链上首版仅支持现货买入/卖出（无杠杆做空）"), nil
	}
	if !intent.SizeQuote.IsPositive() {
		return rejectedOnchain(intent.ClientID, venue.chain, "链上买入金额必须大于 0"), nil
	}
	if intent.ApprovalAmount != nil && intent.ApprovalAmount.Cmp(intent.SizeQuote) != 0 {
		return rejectedOnchain(intent.ClientID, venue.chain, "授权额度必须精确等于本次交易金额"), nil
	}
	venue.mu.RLock()
	_, alreadyOpen := venue.positions[intent.Symbol]
	free := venue.freeQuote
	venue.mu.RUnlock()
	if alreadyOpen {
		return rejectedOnchain(intent.ClientID, venue.chain, "该链上标的已有持仓"), nil
	}
	allowance, err := venue.client.Allowance(ctx, venue.signerAddress(), intent.Symbol, quote.Router)
	if err != nil {
		return contracts.ExecutionResult{}, err
	}
	approvalNeeded := allowance.Cmp(intent.SizeQuote) < 0
	estimatedGas := quote.GasQuote
	if approvalNeeded {
		estimatedGas = estimatedGas.Mul(contracts.NewDecimalFromInt64(2))
	}
	if free.Cmp(intent.SizeQuote.Add(estimatedGas)) < 0 {
		return rejectedOnchain(intent.ClientID, venue.chain, "链上可用余额不足（含预计 gas）"), nil
	}
	gasTotal := contracts.Zero()
	if approvalNeeded {
		nonce, nonceErr := venue.nextNonce(ctx)
		if nonceErr != nil {
			return contracts.ExecutionResult{}, nonceErr
		}
		txHash, sendErr := venue.client.SendApprove(
			ctx, venue.signer, intent.Symbol, quote.Router, intent.SizeQuote, nonce,
		)
		if sendErr != nil {
			return contracts.ExecutionResult{}, sendErr
		}
		receipt, receiptErr := venue.trackReceipt(ctx, txHash, "approve")
		if receiptErr != nil {
			return contracts.ExecutionResult{}, receiptErr
		}
		if receipt == nil || receipt.Status != 1 {
			return failedOnchain(intent.ClientID, venue.chain, "approve 交易 revert", txHash), nil
		}
		gasTotal = gasTotal.Add(receipt.GasUsedQuote)
	}
	sizeBase, err := intent.SizeQuote.Quo(quote.Price)
	if err != nil {
		return contracts.ExecutionResult{}, err
	}
	minimumOut := sizeBase.Mul(contracts.NewDecimalFromInt64(1).Sub(venue.minOutTolerance))
	nonce, err := venue.nextNonce(ctx)
	if err != nil {
		return contracts.ExecutionResult{}, err
	}
	txHash, err := venue.client.SendSwap(
		ctx, venue.signer, intent.Symbol, intent.Side, intent.SizeQuote, minimumOut, nonce,
	)
	if err != nil {
		return contracts.ExecutionResult{}, err
	}
	receipt, err := venue.trackReceipt(ctx, txHash, "swap")
	if err != nil {
		return contracts.ExecutionResult{}, err
	}
	if receipt == nil || receipt.Status != 1 {
		return failedOnchain(intent.ClientID, venue.chain, "swap 交易 revert（滑点超容忍或流动性变化）", txHash), nil
	}
	gasTotal = gasTotal.Add(receipt.GasUsedQuote)
	chain := venue.chain
	position := contracts.Position{
		Symbol: intent.Symbol, Venue: venue.id, Side: contracts.SideLong,
		Instrument: contracts.InstrumentSpot, SizeBase: sizeBase, EntryPrice: quote.Price,
		Leverage: 1, Chain: &chain, TxHash: stringPointer(txHash),
	}
	venue.mu.Lock()
	venue.freeQuote = venue.freeQuote.Sub(intent.SizeQuote).Sub(gasTotal)
	venue.positions[intent.Symbol] = position
	venue.mu.Unlock()
	impactBPS := quote.PriceImpact.Mul(contracts.NewDecimalFromInt64(10_000))
	return contracts.ExecutionResult{
		ClientID: intent.ClientID, OrderID: stringPointer(txHash), Status: contracts.OrderStatusFilled,
		FilledBase: sizeBase, AvgPrice: decimalPointer(quote.Price), FeeQuote: gasTotal,
		SlippageBPS: decimalPointer(impactBPS), Chain: &chain, TxHash: stringPointer(txHash),
		GasUsed: decimalPointer(gasTotal),
	}, nil
}

func (venue *OnchainVenue) closePosition(
	ctx context.Context,
	intent contracts.OrderIntent,
	quote SwapQuote,
) (contracts.ExecutionResult, error) {
	venue.mu.RLock()
	position, ok := venue.positions[intent.Symbol]
	venue.mu.RUnlock()
	if !ok {
		return rejectedOnchain(intent.ClientID, venue.chain, "无持仓可平"), nil
	}
	nonce, err := venue.nextNonce(ctx)
	if err != nil {
		return contracts.ExecutionResult{}, err
	}
	notional := position.SizeBase.Mul(quote.Price)
	txHash, err := venue.client.SendSwap(
		ctx, venue.signer, intent.Symbol, contracts.SideFlat, notional, position.SizeBase, nonce,
	)
	if err != nil {
		return contracts.ExecutionResult{}, err
	}
	receipt, err := venue.trackReceipt(ctx, txHash, "close")
	if err != nil {
		return contracts.ExecutionResult{}, err
	}
	if receipt == nil || receipt.Status != 1 {
		return failedOnchain(intent.ClientID, venue.chain, "平仓 swap revert", txHash), nil
	}
	venue.mu.Lock()
	delete(venue.positions, intent.Symbol)
	venue.freeQuote = venue.freeQuote.Add(notional).Sub(receipt.GasUsedQuote)
	venue.mu.Unlock()
	chain := venue.chain
	return contracts.ExecutionResult{
		ClientID: intent.ClientID, OrderID: stringPointer(txHash), Status: contracts.OrderStatusFilled,
		FilledBase: position.SizeBase, AvgPrice: decimalPointer(quote.Price), FeeQuote: receipt.GasUsedQuote,
		Chain: &chain, TxHash: stringPointer(txHash), GasUsed: decimalPointer(receipt.GasUsedQuote),
	}, nil
}

func (venue *OnchainVenue) nextNonce(ctx context.Context) (uint64, error) {
	venue.mu.Lock()
	defer venue.mu.Unlock()
	if venue.nonce == nil {
		nonce, err := venue.client.PendingNonce(ctx, venue.signerAddress())
		if err != nil {
			return 0, err
		}
		venue.nonce = &nonce
	}
	value := *venue.nonce
	next := value + 1
	venue.nonce = &next
	return value, nil
}

func (venue *OnchainVenue) trackReceipt(
	ctx context.Context,
	txHash, purpose string,
) (*TransactionReceipt, error) {
	venue.mu.Lock()
	venue.pending[txHash] = purpose
	venue.mu.Unlock()
	receipt, err := venue.client.WaitReceipt(ctx, txHash)
	if err == nil && receipt != nil {
		venue.mu.Lock()
		delete(venue.pending, txHash)
		venue.mu.Unlock()
	}
	return receipt, err
}

func (venue *OnchainVenue) ReconcileOnchain(
	ctx context.Context,
) (OnchainReconcileReport, error) {
	if venue.client == nil {
		return OnchainReconcileReport{}, ErrOnchainNotConfigured
	}
	venue.execMu.Lock()
	defer venue.execMu.Unlock()
	chainNonce, err := venue.client.PendingNonce(ctx, venue.signerAddress())
	if err != nil {
		return OnchainReconcileReport{}, err
	}
	report := OnchainReconcileReport{}
	venue.mu.Lock()
	if venue.nonce != nil && *venue.nonce != chainNonce {
		report.Discrepancies = append(report.Discrepancies, fmt.Sprintf(
			"nonce 本地 %d ≠ 链上 %d，已对齐", *venue.nonce, chainNonce,
		))
	}
	venue.nonce = &chainNonce
	pending := make(map[string]string, len(venue.pending))
	for hash, purpose := range venue.pending {
		pending[hash] = purpose
	}
	venue.mu.Unlock()
	for hash, purpose := range pending {
		receipt, receiptErr := venue.client.WaitReceipt(ctx, hash)
		if receiptErr != nil || receipt == nil {
			continue
		}
		report.Settled = append(report.Settled, fmt.Sprintf("%s %s → status=%d", purpose, hash, receipt.Status))
		venue.mu.Lock()
		delete(venue.pending, hash)
		venue.mu.Unlock()
	}
	venue.mu.RLock()
	for hash := range venue.pending {
		report.Pending = append(report.Pending, hash)
	}
	venue.mu.RUnlock()
	sort.Strings(report.Pending)
	report.Nonce = chainNonce
	return report, nil
}

func (*OnchainVenue) Cancel(ctx context.Context, _ string) error { return contextError(ctx) }

func (venue *OnchainVenue) Close() error {
	if closer, ok := venue.client.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (venue *OnchainVenue) remember(clientID string, result contracts.ExecutionResult) contracts.ExecutionResult {
	venue.mu.Lock()
	venue.fills[clientID] = cloneExecution(result)
	venue.mu.Unlock()
	return cloneExecution(result)
}

func (venue *OnchainVenue) routerAllowed(router string) bool {
	_, ok := venue.whitelist[normalizeAddress(router)]
	return ok
}

func normalizeAddress(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func (venue *OnchainVenue) signerAddress() string {
	if venue.signer == nil {
		return "0x0"
	}
	return venue.signer.Address()
}

func rejectedOnchain(clientID, chain, message string) contracts.ExecutionResult {
	return contracts.ExecutionResult{
		ClientID: clientID, Status: contracts.OrderStatusRejected,
		Error: stringPointer(message), Chain: stringPointer(chain),
	}
}

func failedOnchain(clientID, chain, message, txHash string) contracts.ExecutionResult {
	result := contracts.ExecutionResult{
		ClientID: clientID, Status: contracts.OrderStatusFailed,
		Error: stringPointer(message), Chain: stringPointer(chain),
	}
	if txHash != "" {
		result.OrderID = stringPointer(txHash)
		result.TxHash = stringPointer(txHash)
	}
	return result
}
