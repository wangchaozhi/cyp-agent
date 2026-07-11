package venue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type sentOnchainTransaction struct {
	kind       string
	nonce      uint64
	txHash     string
	amount     contracts.Decimal
	minimumOut contracts.Decimal
}

type mockOnchainClient struct {
	mu         sync.Mutex
	quote      SwapQuote
	chainNonce uint64
	allowance  contracts.Decimal
	sent       []sentOnchainTransaction
	revertSwap bool
	sequence   int
}

func newMockOnchainClient() *mockOnchainClient {
	return &mockOnchainClient{
		quote: SwapQuote{
			Price: contracts.MustDecimal("2000"), PriceImpact: contracts.MustDecimal("0.002"),
			PoolTVLUSD: contracts.MustDecimal("5000000"), Router: "0xRouter",
			GasQuote: contracts.MustDecimal("3"), MEVProtected: true,
		},
		chainNonce: 7,
	}
}

func (client *mockOnchainClient) QuoteSwap(
	context.Context,
	string,
	contracts.Side,
	contracts.Decimal,
) (SwapQuote, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.quote, nil
}

func (client *mockOnchainClient) Allowance(
	context.Context,
	string,
	string,
	string,
) (contracts.Decimal, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.allowance, nil
}

func (client *mockOnchainClient) PendingNonce(context.Context, string) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.chainNonce, nil
}

func (client *mockOnchainClient) next(kind string, nonce uint64, amount, minimumOut contracts.Decimal) string {
	client.sequence++
	hash := fmt.Sprintf("0xtx%d", client.sequence)
	client.sent = append(client.sent, sentOnchainTransaction{
		kind: kind, nonce: nonce, txHash: hash, amount: amount, minimumOut: minimumOut,
	})
	return hash
}

func (client *mockOnchainClient) SendApprove(
	_ context.Context,
	_ Signer,
	_, _ string,
	amount contracts.Decimal,
	nonce uint64,
) (string, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.allowance = amount
	return client.next("approve", nonce, amount, contracts.Zero()), nil
}

func (client *mockOnchainClient) SendSwap(
	_ context.Context,
	_ Signer,
	_ string,
	_ contracts.Side,
	amount, minimumOut contracts.Decimal,
	nonce uint64,
) (string, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.next("swap", nonce, amount, minimumOut), nil
}

func (client *mockOnchainClient) WaitReceipt(
	_ context.Context,
	txHash string,
) (*TransactionReceipt, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	status := uint64(1)
	for _, transaction := range client.sent {
		if transaction.txHash == txHash && transaction.kind == "swap" && client.revertSwap {
			status = 0
		}
	}
	return &TransactionReceipt{Status: status, GasUsedQuote: client.quote.GasQuote}, nil
}

func testSigner(t *testing.T) Signer {
	t.Helper()
	signer, err := NewExternalSigner("test", "0xSigner", "supersecret-key-reference", func(
		context.Context,
		map[string]any,
	) ([]byte, error) {
		return []byte("signed"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func newExecutableOnchain(t *testing.T, client *mockOnchainClient) *OnchainVenue {
	t.Helper()
	venue, err := newOnchainVenue(OnchainConfig{
		Chain: "ethereum", InitialQuote: contracts.MustDecimal("10000"),
		RouterWhitelist: []string{"0xrouter"}, EnableExecution: true, Signer: testSigner(t),
	}, client, true)
	if err != nil {
		t.Fatal(err)
	}
	return venue
}

func onchainIntent(clientID string) contracts.OrderIntent {
	return contracts.OrderIntent{
		ClientID: clientID, Symbol: "ETH/USDC", Venue: "onchain-ethereum",
		Side: contracts.SideLong, Instrument: contracts.InstrumentSpot,
		SizeQuote: contracts.MustDecimal("1000"), Leverage: 1,
	}
}

func TestOnchainExecutionRequiresExplicitSafeDependencies(t *testing.T) {
	if _, err := NewOnchainVenue(OnchainConfig{
		EnableExecution: true, RouterWhitelist: []string{"0xrouter"}, Signer: testSigner(t),
	}, newMockOnchainClient()); !errors.Is(err, ErrOnchainTradingDisabled) {
		t.Fatalf("production constructor bypassed live safety rail: %v", err)
	}
	if _, err := newOnchainVenue(OnchainConfig{EnableExecution: true}, nil, true); err == nil {
		t.Fatal("test execution enabled without client/signer/whitelist")
	}
	client := newMockOnchainClient()
	readOnly, err := NewOnchainVenue(OnchainConfig{RouterWhitelist: []string{"0xrouter"}}, client)
	if err != nil {
		t.Fatal(err)
	}
	if readOnly.ExecutionEnabled() || !readOnly.Caps().ReadOnly {
		t.Fatal("default onchain venue unexpectedly enabled execution")
	}
	result, err := readOnly.Place(context.Background(), onchainIntent("disabled"))
	if err != nil || result.Status != contracts.OrderStatusRejected || result.Error == nil ||
		!strings.Contains(*result.Error, "硬禁用") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestOnchainExactApproveThenSwapAndIdempotency(t *testing.T) {
	client := newMockOnchainClient()
	venue := newExecutableOnchain(t, client)
	intent := onchainIntent("oc1")
	result, err := venue.Place(context.Background(), intent)
	if err != nil || result.Status != contracts.OrderStatusFilled || result.TxHash == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	client.mu.Lock()
	if len(client.sent) != 2 || client.sent[0].kind != "approve" || client.sent[1].kind != "swap" {
		t.Fatalf("sent=%#v", client.sent)
	}
	if client.sent[0].amount.Cmp(intent.SizeQuote) != 0 {
		t.Fatalf("approval=%s, want exact %s", client.sent[0].amount, intent.SizeQuote)
	}
	if client.sent[0].nonce != 7 || client.sent[1].nonce != 8 {
		t.Fatalf("nonces=%d/%d", client.sent[0].nonce, client.sent[1].nonce)
	}
	sentCount := len(client.sent)
	client.mu.Unlock()
	replayed, err := venue.Place(context.Background(), intent)
	if err != nil || replayed.TxHash == nil || *replayed.TxHash != *result.TxHash {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	client.mu.Lock()
	if len(client.sent) != sentCount {
		t.Fatal("idempotent replay sent another transaction")
	}
	client.mu.Unlock()
	positions, _ := venue.Positions(context.Background())
	if len(positions) != 1 || positions[0].SizeBase.Cmp(contracts.MustDecimal("0.5")) != 0 {
		t.Fatalf("positions=%#v", positions)
	}
}

func TestOnchainSkipsApprovalWhenAllowanceEnough(t *testing.T) {
	client := newMockOnchainClient()
	client.allowance = contracts.MustDecimal("5000")
	venue := newExecutableOnchain(t, client)
	result, err := venue.Place(context.Background(), onchainIntent("allowed"))
	if err != nil || result.Status != contracts.OrderStatusFilled {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sent) != 1 || client.sent[0].kind != "swap" || client.sent[0].nonce != 7 {
		t.Fatalf("sent=%#v", client.sent)
	}
}

func TestOnchainGuardsWhitelistMEVAndApprovalAmount(t *testing.T) {
	client := newMockOnchainClient()
	venue := newExecutableOnchain(t, client)

	client.quote.Router = "0xevil"
	report, err := venue.Preflight(context.Background(), onchainIntent("evil"))
	if err != nil || report.OK || !strings.Contains(strings.Join(report.Reasons, " "), "白名单") {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	result, _ := venue.Place(context.Background(), onchainIntent("evil"))
	if result.Status != contracts.OrderStatusRejected {
		t.Fatalf("unsafe router result=%#v", result)
	}

	client.quote.Router = "0xrouter"
	client.quote.MEVProtected = false
	report, _ = venue.Preflight(context.Background(), onchainIntent("mev"))
	if report.OK || !strings.Contains(strings.Join(report.Reasons, " "), "MEV") {
		t.Fatalf("MEV report=%#v", report)
	}

	client.quote.MEVProtected = true
	intent := onchainIntent("approval")
	oversized := contracts.MustDecimal("999999")
	intent.ApprovalAmount = &oversized
	result, _ = venue.Place(context.Background(), intent)
	if result.Status != contracts.OrderStatusRejected || result.Error == nil ||
		!strings.Contains(*result.Error, "精确") {
		t.Fatalf("oversized approval result=%#v", result)
	}
	client.mu.Lock()
	if len(client.sent) != 0 {
		t.Fatalf("unsafe requests emitted transactions: %#v", client.sent)
	}
	client.mu.Unlock()
}

func TestOnchainSwapRevertAndCloseRoundTrip(t *testing.T) {
	t.Run("revert", func(t *testing.T) {
		client := newMockOnchainClient()
		client.revertSwap = true
		venue := newExecutableOnchain(t, client)
		result, err := venue.Place(context.Background(), onchainIntent("revert"))
		if err != nil || result.Status != contracts.OrderStatusFailed || result.Error == nil ||
			!strings.Contains(*result.Error, "revert") {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		positions, _ := venue.Positions(context.Background())
		if len(positions) != 0 {
			t.Fatalf("reverted swap created position: %#v", positions)
		}
	})

	t.Run("close", func(t *testing.T) {
		client := newMockOnchainClient()
		venue := newExecutableOnchain(t, client)
		if result, err := venue.Place(context.Background(), onchainIntent("open")); err != nil || result.Status != contracts.OrderStatusFilled {
			t.Fatalf("open=%#v err=%v", result, err)
		}
		closeIntent := onchainIntent("close")
		closeIntent.ReduceOnly = true
		result, err := venue.Place(context.Background(), closeIntent)
		if err != nil || result.Status != contracts.OrderStatusFilled {
			t.Fatalf("close=%#v err=%v", result, err)
		}
		positions, _ := venue.Positions(context.Background())
		if len(positions) != 0 {
			t.Fatalf("positions=%#v", positions)
		}
	})
}

func TestOnchainQuoteContextAndNonceReconciliation(t *testing.T) {
	client := newMockOnchainClient()
	venue := newExecutableOnchain(t, client)
	contextValue, err := venue.QuoteContext(context.Background(), onchainIntent("context"))
	if err != nil || contextValue.ApprovalAmount == nil ||
		contextValue.ApprovalAmount.Cmp(contracts.MustDecimal("1000")) != 0 ||
		contextValue.ContractAddress == nil || *contextValue.ContractAddress != "0xRouter" {
		t.Fatalf("context=%#v err=%v", contextValue, err)
	}
	if _, err := venue.Place(context.Background(), onchainIntent("open")); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.chainNonce = 12
	client.mu.Unlock()
	report, err := venue.ReconcileOnchain(context.Background())
	if err != nil || report.Nonce != 12 || len(report.Discrepancies) != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestExternalSignerNeverLeaksIdentifier(t *testing.T) {
	const secretReference = "supersecret-key-id-12345"
	signer, err := NewExternalSigner("kms", "0xabc", secretReference, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(signer), secretReference) || strings.Contains(fmt.Sprintf("%#v", signer), secretReference) {
		t.Fatal("signer formatting leaked secret identifier")
	}
	if _, err := signer.SignTransaction(context.Background(), nil); !errors.Is(err, ErrSignerUnavailable) {
		t.Fatalf("error=%v", err)
	}
	if _, err := NewExternalSigner("magic", "0xabc", "id", nil); err == nil {
		t.Fatal("unknown signer kind accepted")
	}
}
