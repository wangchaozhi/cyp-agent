// Package orchestrator owns the Go paper run lifecycle. It is the single
// writer for run state and the only component allowed to turn an approved
// proposal into an OrderIntent.
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/agents"
	"github.com/wangchaozhi/cyp-agent/internal/approval"
	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/control"
	"github.com/wangchaozhi/cyp-agent/internal/data"
	"github.com/wangchaozhi/cyp-agent/internal/events"
	"github.com/wangchaozhi/cyp-agent/internal/llm"
	"github.com/wangchaozhi/cyp-agent/internal/metrics"
	"github.com/wangchaozhi/cyp-agent/internal/observability"
	"github.com/wangchaozhi/cyp-agent/internal/orders"
	"github.com/wangchaozhi/cyp-agent/internal/persistence"
	"github.com/wangchaozhi/cyp-agent/internal/portfolio"
	"github.com/wangchaozhi/cyp-agent/internal/risk"
	"github.com/wangchaozhi/cyp-agent/internal/riskstate"
	runtimecore "github.com/wangchaozhi/cyp-agent/internal/runtime"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

var (
	ErrStopped     = errors.New("orchestrator is stopped")
	ErrEmptySymbol = errors.New("symbol must not be empty")
)

type Service struct {
	control      *control.State
	venue        venue.Venue
	events       *events.Bus
	gate         *approval.PendingGate
	metrics      *metrics.Runs
	dataSource   data.Source
	fallbackData data.Source
	repository   persistence.Repository
	riskState    *riskstate.Tracker
	safety       *runtimecore.SafetyState

	pipelineMu  sync.RWMutex
	llm         *llm.Client
	analysts    []agents.Analyst
	strategist  *agents.Strategist
	riskOfficer agents.RiskOfficer
	reviewer    agents.Reviewer

	ctx     context.Context
	cancel  context.CancelFunc
	sem     chan struct{}
	locks   *runtimecore.SymbolLocks
	journal *orders.Journal

	mu      sync.RWMutex
	runs    map[string]contracts.RunResult
	marks   map[string]contracts.Decimal
	stopped bool
	wg      sync.WaitGroup
}

type Option func(*Service)

func WithDataSource(source data.Source) Option {
	return func(service *Service) {
		if source != nil {
			service.dataSource = source
		}
	}
}

func WithRepository(repository persistence.Repository) Option {
	return func(service *Service) { service.repository = repository }
}

func WithRiskState(tracker *riskstate.Tracker) Option {
	return func(service *Service) { service.riskState = tracker }
}

func WithSafety(safety *runtimecore.SafetyState) Option {
	return func(service *Service) { service.safety = safety }
}

func WithLLM(client *llm.Client) Option {
	return func(service *Service) { service.llm = client }
}

// WithSymbolLocks shares one per-symbol lock instance with the runtime
// scanner and reconciliation paths so that every action on a symbol is
// serialized by a single mechanism instead of two independent lock maps.
func WithSymbolLocks(locks *runtimecore.SymbolLocks) Option {
	return func(service *Service) {
		if locks != nil {
			service.locks = locks
		}
	}
}

func New(
	parent context.Context,
	state *control.State,
	executionVenue venue.Venue,
	bus *events.Bus,
	gate *approval.PendingGate,
	runMetrics *metrics.Runs,
	options ...Option,
) *Service {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	concurrency := state.Settings().MaxConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	service := &Service{
		control: state, venue: executionVenue, events: bus, gate: gate, metrics: runMetrics,
		ctx: ctx, cancel: cancel, sem: make(chan struct{}, concurrency),
		locks:        runtimecore.NewSymbolLocks(),
		journal:      orders.NewJournal(),
		runs:         make(map[string]contracts.RunResult),
		marks:        make(map[string]contracts.Decimal),
		dataSource:   data.NewSyntheticMarketData(data.WithLiveTicks(true)),
		fallbackData: data.NewSyntheticMarketData(data.WithLiveTicks(true)),
		llm:          llm.FromSettings(state.Settings()), analysts: agents.AllAnalysts(),
		strategist: agents.NewStrategist(nil), reviewer: agents.NewReviewer(),
	}
	for _, option := range options {
		option(service)
	}
	return service
}

// SetLLM atomically replaces the provider used for future runs. Active runs
// keep their isolated session and budget.
func (s *Service) SetLLM(client *llm.Client) {
	s.pipelineMu.Lock()
	s.llm = client
	s.pipelineMu.Unlock()
}

func (s *Service) LLMMetrics() llm.MetricsSnapshot {
	s.pipelineMu.RLock()
	defer s.pipelineMu.RUnlock()
	if s.llm == nil {
		return llm.MetricsSnapshot{}
	}
	return s.llm.Metrics()
}

// ReviewClosed completes lifecycle attribution after a position has actually
// been closed. Entry execution review remains available on the original run,
// while this review carries realized PnL and durable lessons.
func (s *Service) ReviewClosed(
	ctx context.Context,
	position contracts.Position,
	execution contracts.ExecutionResult,
	pnl contracts.Decimal,
	reference string,
) (contracts.TradeReview, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	review, err := s.reviewer.RunClosed(ctx, position, execution, pnl, reference)
	if err != nil {
		return contracts.TradeReview{}, err
	}
	if s.repository != nil {
		if err := s.repository.AppendLessons(ctx, position.Symbol, review.Lessons); err != nil {
			return contracts.TradeReview{}, fmt.Errorf("persist close lessons: %w", err)
		}
		checkpointID := reference
		if checkpointID == "" {
			checkpointID = execution.ClientID
		}
		if err := s.checkpoint(ctx, checkpointID, "close_review", review); err != nil {
			return contracts.TradeReview{}, err
		}
	}
	s.events.Emit("reviewed", reference, map[string]any{"symbol": position.Symbol, "review": review})
	return review, nil
}

// Start accepts a run immediately and executes it in a bounded goroutine.
func (s *Service) Start(symbol string) (contracts.RunAccepted, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return contracts.RunAccepted{}, ErrEmptySymbol
	}
	runID, err := newRunID()
	if err != nil {
		return contracts.RunAccepted{}, fmt.Errorf("generate run id: %w", err)
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return contracts.RunAccepted{}, ErrStopped
	}
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-s.ctx.Done():
			s.finishCanceled(runID, symbol, s.ctx.Err())
			return
		}
		err := s.locks.Do(s.ctx, symbol, func(runContext context.Context) error {
			s.runAndRecord(runContext, runID, symbol)
			return nil
		})
		if err != nil {
			s.finishCanceled(runID, symbol, err)
		}
	}()
	return contracts.RunAccepted{RunID: runID, Symbol: symbol}, nil
}

// RunOnce is the synchronous test/worker entry point.
func (s *Service) RunOnce(ctx context.Context, runID, symbol string) contracts.RunResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if runID == "" {
		generated, err := newRunID()
		if err != nil {
			message := err.Error()
			return contracts.RunResult{RunID: runID, Symbol: symbol, Status: contracts.RunError, Error: &message}
		}
		runID = generated
	}
	return s.run(ctx, runID, strings.TrimSpace(symbol))
}

func (s *Service) GetRun(runID string) (contracts.RunResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, ok := s.runs[runID]
	return result, ok
}

func (s *Service) Mark(symbol string) (contracts.Decimal, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	mark, ok := s.marks[symbol]
	return mark, ok
}

func (s *Service) Marks() map[string]contracts.Decimal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]contracts.Decimal, len(s.marks))
	for symbol, mark := range s.marks {
		result[symbol] = mark
	}
	return result
}

func (s *Service) Close() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Service) runAndRecord(ctx context.Context, runID, symbol string) {
	trace := observability.NewTrace(runID)
	ctx = observability.ContextWithTrace(ctx, trace)
	result := s.run(ctx, runID, symbol)
	s.mu.Lock()
	s.runs[runID] = result
	s.mu.Unlock()

	var slippage *float64
	if result.Execution != nil && result.Execution.SlippageBPS != nil {
		if value, err := result.Execution.SlippageBPS.Float64(); err == nil {
			slippage = &value
		}
	}
	s.metrics.Record(string(result.Status), slippage)
	_ = s.checkpoint(ctx, runID, "result", result)
	if result.Status == contracts.RunError && result.Error != nil {
		s.events.Emit("run_failed", runID, map[string]any{"symbol": symbol, "error": *result.Error})
	}
	s.events.Emit("run_done", runID, map[string]any{
		"symbol": symbol, "status": result.Status,
		"trace": trace.Summary(),
	})
}

func (s *Service) finishCanceled(runID, symbol string, err error) {
	message := "run canceled"
	if err != nil {
		message = err.Error()
	}
	result := contracts.RunResult{RunID: runID, Symbol: symbol, Status: contracts.RunError, Error: &message}
	s.mu.Lock()
	s.runs[runID] = result
	s.mu.Unlock()
	s.metrics.Record(string(result.Status), nil)
	s.events.Emit("run_failed", runID, map[string]any{"symbol": symbol, "error": message})
	s.events.Emit("run_done", runID, map[string]any{"symbol": symbol, "status": result.Status})
}

func (s *Service) run(ctx context.Context, runID, symbol string) contracts.RunResult {
	result := contracts.RunResult{
		RunID: runID, Symbol: symbol, Status: contracts.RunError,
		Reports: contracts.List[contracts.AnalystReport]{},
	}
	if symbol == "" {
		message := ErrEmptySymbol.Error()
		result.Error = &message
		return result
	}
	if err := ctx.Err(); err != nil {
		message := err.Error()
		result.Error = &message
		return result
	}

	s.events.Emit("run_started", runID, map[string]any{"symbol": symbol})
	settings := s.control.Settings()

	snapshotSpan := startSpan(ctx, "snapshot")
	snapshot, snapshotErr := s.dataSource.Snapshot(ctx, symbol)
	if snapshotErr != nil && s.fallbackData != nil {
		snapshot, snapshotErr = s.fallbackData.Snapshot(ctx, symbol)
	}
	snapshotSpan.End(snapshotErr)
	if snapshotErr != nil {
		return fail(result, fmt.Errorf("snapshot: %w", snapshotErr))
	}
	if snapshot.Symbol == "" {
		snapshot.Symbol = symbol
	}
	mark := referencePrice(symbol)
	if len(snapshot.OHLCV) > 0 && snapshot.OHLCV[len(snapshot.OHLCV)-1].Close.IsPositive() {
		mark = snapshot.OHLCV[len(snapshot.OHLCV)-1].Close
	}
	s.setMark(symbol, mark)
	s.events.Emit("snapshot_ready", runID, map[string]any{"symbol": symbol, "bars": len(snapshot.OHLCV)})

	balances, err := s.venue.Balances(ctx)
	if err != nil {
		return fail(result, fmt.Errorf("balances: %w", err))
	}
	equity := balances.TotalQuote
	if !equity.IsPositive() {
		equity = balances.FreeQuote
	}
	accountRisk := riskstate.Snapshot{CurrentEquity: equity}
	if s.riskState != nil {
		if err := s.riskState.ObserveEquity(ctx, equity); err != nil {
			return fail(result, fmt.Errorf("persist risk state: %w", err))
		}
		accountRisk = s.riskState.Snapshot(equity)
	}
	positions, err := s.venue.Positions(ctx)
	if err != nil {
		return fail(result, fmt.Errorf("positions: %w", err))
	}
	lessons := []string{}
	if s.repository != nil {
		if loaded, loadErr := s.repository.GetLessons(ctx, 10, symbol); loadErr == nil {
			lessons = loaded
		}
	}
	agentContext := agents.AgentContext{
		LLM: s.newLLMSession(), AllowPerp: settings.AllowPerp, Lessons: lessons,
	}

	analystSpan := startSpan(ctx, "analysts")
	reports, err := agents.RunAnalysts(ctx, s.analystsSnapshot(), snapshot, agentContext)
	analystSpan.End(err)
	if err != nil {
		return fail(result, fmt.Errorf("analysts: %w", err))
	}

	result.Reports = reports
	s.events.Emit("reports_ready", runID, map[string]any{"symbol": symbol, "reports": reports})

	strategySpan := startSpan(ctx, "strategy")
	proposal, err := s.strategist.Run(ctx, reports, snapshot, equity, settings.Risk,
		agentContext, s.venue.ID(), positions)
	strategySpan.End(err)
	if err != nil {
		return fail(result, fmt.Errorf("strategy: %w", err))
	}
	result.Proposal = &proposal
	s.events.Emit("proposal_ready", runID, map[string]any{"symbol": symbol, "proposal": proposal})
	if err := s.checkpoint(ctx, runID, "proposal", proposal); err != nil {
		return fail(result, err)
	}
	if proposal.Side == contracts.SideFlat {
		result.Status = contracts.RunNoTrade
		return result
	}

	if safetyErr := s.checkNewPosition(settings); safetyErr != nil {
		assessment := contracts.RiskAssessment{
			Verdict:        contracts.VerdictRejected,
			HardViolations: contracts.List[string]{"runtime_safety: " + safetyErr.Error()},
			LLMNotes:       "", RiskScore: 1, LLMReviewed: false,
		}
		result.Assessment = &assessment
		result.Status = contracts.RunRejected
		s.events.Emit("risk_assessed", runID, map[string]any{"symbol": symbol, "assessment": assessment})
		return result
	}

	preflightSpan := startSpan(ctx, "preflight")
	preflightIntent := intentFor(runID+"-pf", proposal, proposal.SizeQuote)
	preflight, err := s.venue.Preflight(ctx, preflightIntent)
	preflightSpan.End(err)
	if err != nil {
		return fail(result, fmt.Errorf("preflight: %w", err))
	}
	if !preflight.OK {
		assessment := contracts.RiskAssessment{
			Verdict:        contracts.VerdictRejected,
			HardViolations: contracts.List[string]{"preflight: " + strings.Join(preflight.Reasons, "; ")},
			LLMNotes:       "", RiskScore: 1, LLMReviewed: false,
		}
		result.Assessment = &assessment
		result.Status = contracts.RunRejected
		s.events.Emit("risk_assessed", runID, map[string]any{"symbol": symbol, "assessment": assessment})
		return result
	}

	view := portfolio.Build(positions, s.Marks(), equity, settings.Risk.MaxCorrelatedExposure)
	correlated := portfolio.CorrelatedDirectional(view, symbol, proposal.Side)
	reconciling := false
	if s.safety != nil {
		safetySnapshot := s.safety.Snapshot()
		reconciling = safetySnapshot.Frozen || safetySnapshot.ReconcileActive
	}
	riskContext := contracts.RiskContext{
		EquityQuote: equity, RefPrice: mark,
		GrossExposureQuote:      view.Gross,
		SymbolExposureQuote:     portfolio.SymbolNotional(view, symbol),
		CorrelatedExposureQuote: &correlated,
		PortfolioCVARQuote:      accountRisk.PortfolioCVARQuote,
		OrdersLastHour:          accountRisk.OrdersLastHour,
		ConsecutiveLosses:       accountRisk.ConsecutiveLosses,
		DailyDrawdown:           accountRisk.DailyDrawdown,
		WeeklyDrawdown:          accountRisk.WeeklyDrawdown,
		TotalDrawdown:           accountRisk.TotalDrawdown,
		Kill:                    settings.Kill, Reconciling: reconciling,
		EstimatedSlippageBPS:      preflight.EstSlippageBPS,
		EstimatedLiquidationPrice: preflight.EstLiquidationPrice,
		EstimatedPriceImpact:      preflight.EstPriceImpact,
	}
	limits := limitsFromConfig(settings.Risk)
	riskSpan := startSpan(ctx, "risk")
	assessment := risk.Assess(proposal, riskContext, limits)
	assessment, err = s.riskOfficer.Run(ctx, proposal, assessment, reports, agentContext)
	riskSpan.End(err)
	if err != nil {
		return fail(result, fmt.Errorf("risk officer: %w", err))
	}
	result.Assessment = &assessment
	s.events.Emit("risk_assessed", runID, map[string]any{"symbol": symbol, "assessment": assessment})
	if err := s.checkpoint(ctx, runID, "risk", assessment); err != nil {
		return fail(result, err)
	}
	if assessment.Verdict == contracts.VerdictRejected {
		result.Status = contracts.RunRejected
		return result
	}

	approvalStarted := time.Now()
	decisionResult, err := s.decide(ctx, runID, proposal, assessment, settings)
	s.metrics.RecordApprovalLatency(time.Since(approvalStarted))
	if err != nil {
		return fail(result, fmt.Errorf("approval: %w", err))
	}
	decision := decisionResult.Decision
	result.Decision = &decision
	s.events.Emit("approval_decided", runID, map[string]any{"symbol": symbol, "decision": decision})
	if err := s.checkpoint(ctx, runID, "approval", decision); err != nil {
		return fail(result, err)
	}
	if decision.Decision == contracts.ApprovalReject {
		result.Status = contracts.RunNotApproved
		return result
	}

	finalProposal := decisionResult.FinalProposal
	if assessment.AdjustedSizeQuote != nil && assessment.AdjustedSizeQuote.Cmp(finalProposal.SizeQuote) < 0 {
		finalProposal.SizeQuote = *assessment.AdjustedSizeQuote
	}
	// Every operator modification and every downsizing decision is revalidated.
	settings = s.control.Settings()
	riskContext.Kill = settings.Kill
	finalAssessment := risk.Assess(finalProposal, riskContext, limitsFromConfig(settings.Risk))
	if finalAssessment.Verdict == contracts.VerdictRejected {
		result.Assessment = &finalAssessment
		result.Proposal = &finalProposal
		result.Status = contracts.RunRejected
		s.events.Emit("risk_assessed", runID, map[string]any{"symbol": symbol, "assessment": finalAssessment})
		return result
	}
	if finalAssessment.AdjustedSizeQuote != nil && finalAssessment.AdjustedSizeQuote.Cmp(finalProposal.SizeQuote) < 0 {
		finalProposal.SizeQuote = *finalAssessment.AdjustedSizeQuote
	}
	if safetyErr := s.checkNewPosition(settings); safetyErr != nil {
		return fail(result, fmt.Errorf("execution blocked by runtime safety control: %w", safetyErr))
	}
	result.Proposal = &finalProposal

	orderIntent := intentFor(runID, finalProposal, finalProposal.SizeQuote)
	if err := s.checkpoint(ctx, runID, "order_intent", orderIntent); err != nil {
		return fail(result, err)
	}
	_ = s.journal.Open(runID+":open", orderIntent)
	_ = s.journal.Transition(runID+":submit", orderIntent.ClientID, contracts.OrderStatusSubmitting, nil, "")
	executionSpan := startSpan(ctx, "execution")
	execution, err := s.venue.Place(ctx, orderIntent)
	executionSpan.End(err)
	if err != nil {
		_ = s.journal.Transition(runID+":fail", orderIntent.ClientID, contracts.OrderStatusFailed, nil, err.Error())
		return fail(result, fmt.Errorf("execute: %w", err))
	}
	s.recordExecution(runID, orderIntent.ClientID, execution)
	result.Execution = &execution
	s.events.Emit("executed", runID, map[string]any{"symbol": symbol, "execution": execution})
	if err := s.checkpoint(ctx, runID, "execution", execution); err != nil {
		return fail(result, err)
	}
	if s.riskState != nil && execution.Status == contracts.OrderStatusFilled {
		balancesAfter, balanceErr := s.venue.Balances(ctx)
		if balanceErr != nil {
			return fail(result, fmt.Errorf("post-execution balances: %w", balanceErr))
		}
		equityAfter := balancesAfter.TotalQuote
		if !equityAfter.IsPositive() {
			equityAfter = balancesAfter.FreeQuote
		}
		if err := s.riskState.RecordOpen(ctx, runID, finalProposal, execution, equityAfter); err != nil {
			return fail(result, fmt.Errorf("persist executed trade risk state: %w", err))
		}
	}

	reviewSpan := startSpan(ctx, "review")
	review, err := s.reviewer.Run(ctx, finalProposal, execution, agentContext, runID)
	reviewSpan.End(err)
	if err != nil {
		return fail(result, fmt.Errorf("review: %w", err))
	}
	result.Review = &review
	s.events.Emit("reviewed", runID, map[string]any{"symbol": symbol, "review": review})
	if s.repository != nil {
		if err := s.repository.AppendLessons(ctx, symbol, review.Lessons); err != nil {
			return fail(result, fmt.Errorf("persist lessons: %w", err))
		}
	}
	if execution.Status == contracts.OrderStatusFilled {
		result.Status = contracts.RunExecuted
	} else {
		result.Status = contracts.RunExecutionFailed
	}
	return result
}

func (s *Service) decide(
	ctx context.Context,
	runID string,
	proposal contracts.TradeProposal,
	assessment contracts.RiskAssessment,
	settings config.Settings,
) (approval.DecisionResult, error) {
	if settings.Approval == "auto" && autoAllowed(settings, proposal, assessment) {
		decision := contracts.ApprovalDecision{
			Decision: contracts.ApprovalApprove, Operator: "auto-policy",
			TS: time.Now().UTC(), Note: "自动审批策略通过",
		}
		return approval.DecisionResult{Decision: decision, FinalProposal: proposal}, nil
	}
	return s.gate.Decide(ctx, runID, proposal, assessment)
}

func autoAllowed(settings config.Settings, proposal contracts.TradeProposal, assessment contracts.RiskAssessment) bool {
	allowed := false
	for _, symbol := range settings.AutoSymbolsList() {
		if symbol == proposal.Symbol {
			allowed = true
			break
		}
	}
	return allowed && assessment.RiskScore <= settings.AutoMaxRiskScore && proposal.SizeQuote.Cmp(settings.AutoMaxQuote) <= 0
}

func (s *Service) newLLMSession() agents.LLM {
	s.pipelineMu.RLock()
	base := s.llm
	s.pipelineMu.RUnlock()
	if base == nil {
		return nil
	}
	return base.NewSession()
}

func (s *Service) analystsSnapshot() []agents.Analyst {
	s.pipelineMu.RLock()
	defer s.pipelineMu.RUnlock()
	return append([]agents.Analyst(nil), s.analysts...)
}

func (s *Service) checkNewPosition(settings config.Settings) error {
	if s.safety != nil {
		return s.safety.CheckNewPosition(runtimecore.RuntimeState{
			Mode: settings.Mode, ExecutionVenue: settings.ExecutionVenue, Kill: settings.Kill,
		})
	}
	if !settings.NewPaperPositionAllowed() {
		return errors.New("only mode=paper and execution_venue=paper may open positions")
	}
	return nil
}

// recordExecution journals the venue outcome. The journal only accepts legal
// transitions, so a venue reporting an unexpected status leaves the order in
// its last consistent state instead of corrupting the log.
func (s *Service) recordExecution(runID, clientID string, execution contracts.ExecutionResult) {
	status := execution.Status
	if !orders.CanTransition(contracts.OrderStatusSubmitting, status) {
		status = contracts.OrderStatusUnknown
	}
	_ = s.journal.Transition(runID+":result", clientID, status, &execution, "")
	if execution.Status == contracts.OrderStatusFilled && len(execution.ProtectiveOrders) > 0 {
		_ = s.journal.Transition(runID+":protective", clientID, contracts.OrderStatusProtectivePlaced, nil, "")
	}
}

// Order exposes the journaled state of one order for reconciliation and the
// API layer.
func (s *Service) Order(clientID string) (orders.Order, bool) {
	return s.journal.Get(clientID)
}

// UnresolvedOrders lists journaled orders that still need reconciliation.
func (s *Service) UnresolvedOrders() []orders.Order {
	return s.journal.Unresolved()
}

func (s *Service) checkpoint(ctx context.Context, runID, step string, value any) error {
	if s.repository == nil {
		return nil
	}
	if err := s.repository.SaveCheckpoint(ctx, runID, step, value); err != nil {
		return fmt.Errorf("persist checkpoint %s: %w", step, err)
	}
	return nil
}

func startSpan(ctx context.Context, name string) *observability.Span {
	trace, ok := observability.TraceFromContext(ctx)
	if !ok {
		return &observability.Span{}
	}
	return trace.StartSpan(name)
}

func (s *Service) setMark(symbol string, mark contracts.Decimal) {
	s.mu.Lock()
	s.marks[symbol] = mark
	s.mu.Unlock()
	if setter, ok := s.venue.(interface {
		SetMarkPrice(string, contracts.Decimal)
	}); ok {
		setter.SetMarkPrice(symbol, mark)
		return
	}
	if setter, ok := s.venue.(interface {
		SetMarkPrice(string, contracts.Decimal) error
	}); ok {
		_ = setter.SetMarkPrice(symbol, mark)
	}
}

func referencePrice(symbol string) contracts.Decimal {
	base := strings.ToUpper(strings.SplitN(symbol, "/", 2)[0])
	switch base {
	case "BTC":
		return contracts.MustDecimal("60000")
	case "ETH":
		return contracts.MustDecimal("3000")
	default:
		return contracts.MustDecimal("100")
	}
}

func intentFor(clientID string, proposal contracts.TradeProposal, size contracts.Decimal) contracts.OrderIntent {
	return contracts.OrderIntent{
		ClientID: clientID, Symbol: proposal.Symbol, Venue: "paper",
		Side: proposal.Side, Instrument: proposal.Instrument, OrderType: proposal.Entry.Type,
		SizeQuote: size, Leverage: proposal.Leverage, MarginMode: proposal.MarginMode,
		StopLoss:   proposal.StopLoss,
		TakeProfit: append(contracts.List[contracts.Decimal]{}, proposal.TakeProfit...),
	}
}

func limitsFromConfig(value config.RiskConfig) risk.Limits {
	return risk.Limits{
		MaxRiskPerTrade:        value.MaxRiskPerTrade,
		MaxPositionPct:         value.MaxPositionPct,
		MaxGrossExposure:       value.MaxGrossExposure,
		MaxSymbolConcentration: value.MaxSymbolConcentration,
		MaxCorrelatedExposure:  value.MaxCorrelatedExposure,
		MaxCVARPct:             value.MaxCVARPct,
		MaxOrdersPerHour:       value.MaxOrdersPerHour,
		MaxSlippageBPS:         value.MaxSlippageBPS,
		MaxLeverage:            value.MaxLeverage,
		MinLiquidationBuffer:   value.MinLiqBuffer,
		ForceIsolated:          value.ForceIsolated,
		MinMarginRatio:         value.MinMarginRatio,
		MaxPriceImpact:         value.MaxPriceImpact,
		MaxGasQuote:            value.MaxGasQuote,
		MinPoolTVL:             value.MinPoolTVL,
		ContractWhitelist:      value.ContractWhitelistSet(),
		RequirePrivateMempool:  value.RequirePrivateMempool,
		DailyDrawdownLimit:     value.DailyDrawdownLimit,
		WeeklyDrawdownLimit:    value.WeeklyDrawdownLimit,
		MaxDrawdownLimit:       value.MaxDrawdownLimit,
		MaxConsecutiveLosses:   value.MaxConsecutiveLosses,
	}
}

func fail(result contracts.RunResult, err error) contracts.RunResult {
	message := err.Error()
	result.Status = contracts.RunError
	result.Error = &message
	return result
}

func newRunID() (string, error) {
	var bytes [6]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
