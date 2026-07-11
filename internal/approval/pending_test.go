package approval

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/events"
)

func proposal() contracts.TradeProposal {
	stop := contracts.MustDecimal("58000")
	return contracts.TradeProposal{
		Symbol:     "BTC/USDT",
		Venue:      "paper",
		Side:       contracts.SideLong,
		Instrument: contracts.InstrumentSpot,
		SizeQuote:  contracts.MustDecimal("1000"),
		Leverage:   1,
		MarginMode: contracts.MarginModeIsolated,
		Entry:      contracts.PricePlan{Type: contracts.EntryTypeMarket},
		StopLoss:   &stop,
		Confidence: 0.6,
	}
}

func assessment() contracts.RiskAssessment {
	return contracts.RiskAssessment{Verdict: contracts.VerdictApproved, RiskScore: 0.2}
}

func waitPending(t *testing.T, gate *PendingGate, runID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, item := range gate.ListPending() {
			if item.RunID == runID {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run %s did not become pending", runID)
}

func TestPendingGateApproveAndOperator(t *testing.T) {
	bus := events.NewBus(4)
	sub := bus.Subscribe(1)
	defer sub.Cancel()
	gate := NewPendingGate(time.Second, bus)
	resultC := make(chan DecisionResult, 1)
	errC := make(chan error, 1)
	go func() {
		result, err := gate.Decide(context.Background(), "r1", proposal(), assessment())
		resultC <- result
		errC <- err
	}()
	waitPending(t, gate, "r1")
	if err := gate.Resolve("r1", contracts.ApprovalRequest{
		Decision: contracts.ApprovalApprove,
		Operator: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	result := <-resultC
	if err := <-errC; err != nil {
		t.Fatal(err)
	}
	if result.Decision.Decision != contracts.ApprovalApprove || result.Decision.Operator != "alice" {
		t.Fatalf("unexpected decision: %#v", result.Decision)
	}
	if result.FinalProposal.SizeQuote.Cmp(contracts.MustDecimal("1000")) != 0 || result.RequiresRevalidation {
		t.Fatalf("unexpected approve result: %#v", result)
	}
	if gate.PendingCount() != 0 {
		t.Fatal("resolved approval remained pending")
	}
	select {
	case event := <-sub.C:
		if event.Type != "awaiting_approval" || event.RunID != "r1" {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing awaiting_approval event")
	}
}

func TestPendingGateModifyReturnsFinalProposalForRevalidation(t *testing.T) {
	gate := NewPendingGate(time.Second, nil)
	resultC := make(chan DecisionResult, 1)
	go func() {
		result, _ := gate.Decide(context.Background(), "r2", proposal(), assessment())
		resultC <- result
	}()
	waitPending(t, gate, "r2")
	size := contracts.MustDecimal("500.25")
	if err := gate.Resolve("r2", contracts.ApprovalRequest{
		Decision: contracts.ApprovalModify,
		Size:     &size,
	}); err != nil {
		t.Fatal(err)
	}
	result := <-resultC
	if !result.RequiresRevalidation {
		t.Fatal("modified proposal was not marked for revalidation")
	}
	if result.FinalProposal.SizeQuote.Cmp(size) != 0 {
		t.Fatalf("final size=%s, want %s", result.FinalProposal.SizeQuote, size)
	}
	if result.Decision.Modified == nil || result.Decision.Modified.SizeQuote.Cmp(size) != 0 {
		t.Fatalf("audit decision omitted modified proposal: %#v", result.Decision)
	}
	if result.Decision.Operator != "dashboard" {
		t.Fatalf("operator=%q, want dashboard", result.Decision.Operator)
	}
}

func TestPendingGateRejectAndTimeout(t *testing.T) {
	t.Run("explicit", func(t *testing.T) {
		gate := NewPendingGate(time.Second, nil)
		resultC := make(chan DecisionResult, 1)
		go func() {
			result, _ := gate.Decide(context.Background(), "reject", proposal(), assessment())
			resultC <- result
		}()
		waitPending(t, gate, "reject")
		if err := gate.Resolve("reject", contracts.ApprovalRequest{Decision: contracts.ApprovalReject}); err != nil {
			t.Fatal(err)
		}
		if result := <-resultC; result.Decision.Decision != contracts.ApprovalReject {
			t.Fatalf("decision=%q, want reject", result.Decision.Decision)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		gate := NewPendingGate(10*time.Millisecond, nil)
		result, err := gate.Decide(context.Background(), "timeout", proposal(), assessment())
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Decision != contracts.ApprovalReject || result.Decision.Note != "审批超时(fail-safe)" {
			t.Fatalf("unexpected timeout result: %#v", result.Decision)
		}
		if gate.PendingCount() != 0 {
			t.Fatal("timed-out approval remained pending")
		}
	})
}

func TestInvalidResolveDoesNotConsumePending(t *testing.T) {
	gate := NewPendingGate(time.Second, nil)
	resultC := make(chan DecisionResult, 1)
	go func() {
		result, _ := gate.Decide(context.Background(), "r3", proposal(), assessment())
		resultC <- result
	}()
	waitPending(t, gate, "r3")
	if err := gate.Resolve("r3", contracts.ApprovalRequest{Decision: contracts.ApprovalModify}); !errors.Is(err, ErrModifySizeNeeded) {
		t.Fatalf("error=%v, want ErrModifySizeNeeded", err)
	}
	if gate.PendingCount() != 1 {
		t.Fatal("invalid request consumed pending approval")
	}
	if err := gate.Resolve("r3", contracts.ApprovalRequest{Decision: contracts.ApprovalReject}); err != nil {
		t.Fatal(err)
	}
	<-resultC
	if err := gate.Resolve("r3", contracts.ApprovalRequest{Decision: contracts.ApprovalApprove}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second resolve error=%v, want ErrNotPending", err)
	}
}

func TestDuplicateRunIDAndContextCancellation(t *testing.T) {
	gate := NewPendingGate(time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := gate.Decide(ctx, "same", proposal(), assessment())
		firstDone <- err
	}()
	waitPending(t, gate, "same")
	if _, err := gate.Decide(context.Background(), "same", proposal(), assessment()); !errors.Is(err, ErrDuplicateRunID) {
		t.Fatalf("duplicate error=%v, want ErrDuplicateRunID", err)
	}
	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v, want context.Canceled", err)
	}
	if gate.PendingCount() != 0 {
		t.Fatal("canceled approval remained pending")
	}
}

func TestConcurrentResolveOnlyOneWins(t *testing.T) {
	gate := NewPendingGate(time.Second, nil)
	resultC := make(chan DecisionResult, 1)
	go func() {
		result, _ := gate.Decide(context.Background(), "race", proposal(), assessment())
		resultC <- result
	}()
	waitPending(t, gate, "race")

	var wg sync.WaitGroup
	var successes int
	var mu sync.Mutex
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if gate.Resolve("race", contracts.ApprovalRequest{Decision: contracts.ApprovalApprove}) == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("successful resolves=%d, want 1", successes)
	}
	if result := <-resultC; result.Decision.Decision != contracts.ApprovalApprove {
		t.Fatalf("unexpected winning result: %#v", result)
	}
}
