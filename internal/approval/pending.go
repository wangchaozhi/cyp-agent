// Package approval implements human-in-the-loop approval gates.
package approval

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/events"
)

const DefaultTimeout = 30 * time.Minute

var (
	ErrEmptyRunID       = errors.New("approval run_id is required")
	ErrDuplicateRunID   = errors.New("approval run_id is already pending")
	ErrNotPending       = errors.New("approval is not pending or was already resolved")
	ErrInvalidDecision  = errors.New("invalid approval decision")
	ErrModifySizeNeeded = errors.New("modify decision requires a positive size")
)

// DecisionResult gives the orchestrator both the audit decision and the exact
// proposal it should inspect next. RequiresRevalidation is true after a human
// modification; callers must run deterministic risk checks again before order
// placement.
type DecisionResult struct {
	Decision             contracts.ApprovalDecision `json:"decision"`
	FinalProposal        contracts.TradeProposal    `json:"final_proposal"`
	RequiresRevalidation bool                       `json:"requires_revalidation"`
}

// PendingApproval is the JSON-compatible snapshot returned by /api/pending.
type PendingApproval struct {
	RunID      string                   `json:"run_id"`
	Proposal   contracts.TradeProposal  `json:"proposal"`
	Assessment contracts.RiskAssessment `json:"assessment"`
}

type pendingEntry struct {
	sequence   uint64
	proposal   contracts.TradeProposal
	assessment contracts.RiskAssessment
	result     chan DecisionResult
}

// PendingGate stores pending decisions in memory. It is safe for concurrent
// API handlers and orchestrator goroutines; each run can be completed once.
type PendingGate struct {
	mu              sync.Mutex
	pending         map[string]*pendingEntry
	nextSequence    uint64
	timeout         time.Duration
	defaultOperator string
	events          *events.Bus
	now             func() time.Time
}

// PendingApprovalGate retains the explicit domain name used by the existing
// server while PendingGate remains convenient at call sites.
type PendingApprovalGate = PendingGate

// GateOption customizes a PendingGate.
type GateOption func(*PendingGate)

// WithDefaultOperator changes the audit operator used when a request omits it.
func WithDefaultOperator(operator string) GateOption {
	return func(g *PendingGate) {
		if operator = strings.TrimSpace(operator); operator != "" {
			g.defaultOperator = operator
		}
	}
}

// NewPendingGate creates a fail-safe gate. A non-positive timeout uses the
// production default rather than accidentally disabling timeout protection.
func NewPendingGate(timeout time.Duration, bus *events.Bus, options ...GateOption) *PendingGate {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	gate := &PendingGate{
		pending:         make(map[string]*pendingEntry),
		timeout:         timeout,
		defaultOperator: "dashboard",
		events:          bus,
		now:             time.Now,
	}
	for _, option := range options {
		option(gate)
	}
	return gate
}

// NewPendingApprovalGate is the domain-named constructor equivalent.
func NewPendingApprovalGate(
	timeout time.Duration,
	bus *events.Bus,
	options ...GateOption,
) *PendingApprovalGate {
	return NewPendingGate(timeout, bus, options...)
}

// Decide registers a proposal and blocks until an API request resolves it,
// the timeout expires, or ctx is canceled. Timeout is a normal reject decision
// (fail safe), not an error.
func (g *PendingGate) Decide(
	ctx context.Context,
	runID string,
	proposal contracts.TradeProposal,
	assessment contracts.RiskAssessment,
) (DecisionResult, error) {
	if strings.TrimSpace(runID) == "" {
		return DecisionResult{}, ErrEmptyRunID
	}
	if err := proposal.Validate(); err != nil {
		return DecisionResult{}, fmt.Errorf("invalid approval proposal: %w", err)
	}
	if err := assessment.Validate(); err != nil {
		return DecisionResult{}, fmt.Errorf("invalid risk assessment: %w", err)
	}

	entry := &pendingEntry{
		proposal:   cloneProposal(proposal),
		assessment: cloneAssessment(assessment),
		result:     make(chan DecisionResult, 1),
	}
	g.mu.Lock()
	if _, exists := g.pending[runID]; exists {
		g.mu.Unlock()
		return DecisionResult{}, fmt.Errorf("%w: %s", ErrDuplicateRunID, runID)
	}
	g.nextSequence++
	entry.sequence = g.nextSequence
	g.pending[runID] = entry
	g.mu.Unlock()

	if g.events != nil {
		g.events.Emit("awaiting_approval", runID, map[string]any{
			"symbol":     proposal.Symbol,
			"proposal":   cloneProposal(proposal),
			"assessment": cloneAssessment(assessment),
		})
	}

	timer := time.NewTimer(g.timeout)
	defer timer.Stop()
	select {
	case result := <-entry.result:
		return cloneDecisionResult(result), nil
	case <-timer.C:
		if g.removeIfCurrent(runID, entry) {
			return g.rejection(entry.proposal, "审批超时(fail-safe)"), nil
		}
		// A resolver won the race with the timer and removed the entry first.
		return cloneDecisionResult(<-entry.result), nil
	case <-ctx.Done():
		if g.removeIfCurrent(runID, entry) {
			return g.rejection(entry.proposal, "审批上下文已取消(fail-safe)"), ctx.Err()
		}
		return cloneDecisionResult(<-entry.result), nil
	}
}

// Resolve completes one pending run. Business validation errors are returned
// to the API layer; no invalid request consumes the pending item.
func (g *PendingGate) Resolve(runID string, request contracts.ApprovalRequest) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.pending[runID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotPending, runID)
	}
	result, err := g.resultFor(entry.proposal, request)
	if err != nil {
		return err
	}
	delete(g.pending, runID)
	entry.result <- result
	return nil
}

// TryResolve is a compatibility helper for call sites that only need the
// approval gate's boolean success behavior.
func (g *PendingGate) TryResolve(runID string, request contracts.ApprovalRequest) bool {
	return g.Resolve(runID, request) == nil
}

// ListPending returns insertion-ordered independent snapshots.
func (g *PendingGate) ListPending() []PendingApproval {
	type sequenced struct {
		sequence uint64
		item     PendingApproval
	}
	g.mu.Lock()
	items := make([]sequenced, 0, len(g.pending))
	for runID, entry := range g.pending {
		items = append(items, sequenced{
			sequence: entry.sequence,
			item: PendingApproval{
				RunID:      runID,
				Proposal:   cloneProposal(entry.proposal),
				Assessment: cloneAssessment(entry.assessment),
			},
		})
	}
	g.mu.Unlock()
	sort.Slice(items, func(i, j int) bool { return items[i].sequence < items[j].sequence })
	result := make([]PendingApproval, len(items))
	for i := range items {
		result[i] = items[i].item
	}
	return result
}

// PendingCount returns the current number of unresolved approvals.
func (g *PendingGate) PendingCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pending)
}

func (g *PendingGate) resultFor(
	proposal contracts.TradeProposal,
	request contracts.ApprovalRequest,
) (DecisionResult, error) {
	operator := strings.TrimSpace(request.Operator)
	if operator == "" {
		operator = g.defaultOperator
	}
	decision := contracts.ApprovalDecision{
		Decision: request.Decision,
		Operator: operator,
		TS:       g.now().UTC(),
		Note:     request.Note,
	}
	result := DecisionResult{FinalProposal: cloneProposal(proposal)}
	switch request.Decision {
	case contracts.ApprovalApprove:
		if decision.Note == "" {
			decision.Note = "仪表盘批准"
		}
	case contracts.ApprovalReject:
		if decision.Note == "" {
			decision.Note = "仪表盘拒绝"
		}
	case contracts.ApprovalModify:
		if request.Size == nil || !request.Size.IsPositive() {
			return DecisionResult{}, ErrModifySizeNeeded
		}
		modified := cloneProposal(proposal)
		modified.SizeQuote = *request.Size
		decision.Modified = &modified
		result.FinalProposal = cloneProposal(modified)
		result.RequiresRevalidation = true
		if decision.Note == "" {
			decision.Note = fmt.Sprintf("改规模至 %s", request.Size.String())
		}
	default:
		return DecisionResult{}, fmt.Errorf("%w: %q", ErrInvalidDecision, request.Decision)
	}
	result.Decision = decision
	return result, nil
}

func (g *PendingGate) rejection(proposal contracts.TradeProposal, note string) DecisionResult {
	return DecisionResult{
		Decision: contracts.ApprovalDecision{
			Decision: contracts.ApprovalReject,
			Operator: g.defaultOperator,
			TS:       g.now().UTC(),
			Note:     note,
		},
		FinalProposal: cloneProposal(proposal),
	}
}

func (g *PendingGate) removeIfCurrent(runID string, expected *pendingEntry) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending[runID] != expected {
		return false
	}
	delete(g.pending, runID)
	return true
}

func cloneProposal(proposal contracts.TradeProposal) contracts.TradeProposal {
	proposal.TakeProfit = append(contracts.List[contracts.Decimal]{}, proposal.TakeProfit...)
	proposal.SupportingReports = append(contracts.List[string]{}, proposal.SupportingReports...)
	if proposal.StopLoss != nil {
		value := *proposal.StopLoss
		proposal.StopLoss = &value
	}
	if proposal.Entry.Price != nil {
		value := *proposal.Entry.Price
		proposal.Entry.Price = &value
	}
	if proposal.Entry.Low != nil {
		value := *proposal.Entry.Low
		proposal.Entry.Low = &value
	}
	if proposal.Entry.High != nil {
		value := *proposal.Entry.High
		proposal.Entry.High = &value
	}
	return proposal
}

func cloneAssessment(assessment contracts.RiskAssessment) contracts.RiskAssessment {
	assessment.HardViolations = append(contracts.List[string]{}, assessment.HardViolations...)
	if assessment.AdjustedSizeQuote != nil {
		value := *assessment.AdjustedSizeQuote
		assessment.AdjustedSizeQuote = &value
	}
	return assessment
}

func cloneDecisionResult(result DecisionResult) DecisionResult {
	result.FinalProposal = cloneProposal(result.FinalProposal)
	if result.Decision.Modified != nil {
		modified := cloneProposal(*result.Decision.Modified)
		result.Decision.Modified = &modified
	}
	return result
}
