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
	ErrStopped       = errors.New("orchestrator is stopped")
	ErrEmptySymbol   = errors.New("symbol must not be empty")
	ErrRunInProgress = errors.New("a run for this symbol is already in progress")
	ErrRunQueueFull  = errors.New("orchestrator run queue is full")
)

const defaultRunHistoryLimit = 2000

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
	alerter      runtimecore.AlertSender

	pipelineMu  sync.RWMutex
	llm         *llm.Client
	analysts    []agents.Analyst
	strategist  *agents.Strategist
	riskOfficer agents.RiskOfficer
	reviewer    agents.Reviewer

	ctx       context.Context
	cancel    context.CancelFunc
	sem       chan struct{}
	queueSize int
	locks     *runtimecore.SymbolLocks
	journal   *orders.Journal
	reversals *ReversalTracker

	mu       sync.RWMutex
	runs     map[string]contracts.RunResult
	runOrder []string
	runLimit int
	marks    map[string]contracts.Decimal
	inFlight map[string]string
	stopped  bool
	wg       sync.WaitGroup
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

func WithOrderJournal(journal *orders.Journal) Option {
	return func(service *Service) {
		if journal != nil {
			service.journal = journal
		}
	}
}

func WithRiskState(tracker *riskstate.Tracker) Option {
	return func(service *Service) { service.riskState = tracker }
}

func WithSafety(safety *runtimecore.SafetyState) Option {
	return func(service *Service) { service.safety = safety }
}

// WithAlerter routes protective remediation and emergency flatten outcomes to
// the operator alert channel in addition to the SSE event bus.
func WithAlerter(alerter runtimecore.AlertSender) Option {
	return func(service *Service) { service.alerter = alerter }
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

// WithRunHistoryLimit bounds in-memory API lookup history. Durable
// checkpoints remain in the configured repository.
func WithRunHistoryLimit(limit int) Option {
	return func(service *Service) {
		if limit > 0 {
			service.runLimit = limit
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
		ctx: ctx, cancel: cancel, sem: make(chan struct{}, concurrency), queueSize: max(16, concurrency*4),
		locks:        runtimecore.NewSymbolLocks(),
		journal:      orders.NewJournal(),
		reversals:    NewReversalTracker(),
		runs:         make(map[string]contracts.RunResult),
		runLimit:     defaultRunHistoryLimit,
		marks:        make(map[string]contracts.Decimal),
		inFlight:     make(map[string]string),
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
	return s.start(symbol, "manual")
}

// StartAutomated attributes scheduled scanner runs independently from
// dashboard/API runs in provider-neutral LLM usage reports.
func (s *Service) StartAutomated(symbol string) (contracts.RunAccepted, error) {
	return s.start(symbol, "automatic")
}

func (s *Service) start(symbol, source string) (contracts.RunAccepted, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return contracts.RunAccepted{}, ErrEmptySymbol
	}
	// Fail before collecting data or calling an LLM when execution is already
	// unavailable. Automated exits use a separate reduce-only path and remain
	// active while entry analysis is blocked.
	if err := s.checkNewPosition(s.control.Settings()); err != nil {
		return contracts.RunAccepted{}, err
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
	if _, running := s.inFlight[symbol]; running {
		s.mu.Unlock()
		return contracts.RunAccepted{}, ErrRunInProgress
	}
	if len(s.inFlight) >= s.queueSize {
		s.mu.Unlock()
		return contracts.RunAccepted{}, ErrRunQueueFull
	}
	s.inFlight[symbol] = runID
	s.runs[runID] = contracts.RunResult{
		RunID: runID, Symbol: symbol, Status: contracts.RunQueued,
		Reports: contracts.List[contracts.AnalystReport]{},
	}
	s.runOrder = append(s.runOrder, runID)
	s.pruneRunsLocked()
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.inFlight, symbol)
			s.pruneRunsLocked()
			s.mu.Unlock()
			s.wg.Done()
		}()
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-s.ctx.Done():
			s.finishCanceled(runID, symbol, s.ctx.Err())
			return
		}
		s.mu.Lock()
		progress := s.runs[runID]
		progress.Status = contracts.RunRunning
		s.runs[runID] = progress
		s.mu.Unlock()
		err := s.locks.Do(s.ctx, symbol, func(runContext context.Context) error {
			runContext = llm.WithUsageMetadata(runContext, llm.UsageMetadata{
				RunID: runID, Symbol: symbol, Source: source,
			})
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
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	ctx = llm.WithUsageMetadata(ctx, llm.UsageMetadata{RunID: runID, Symbol: symbol, Source: "manual"})
	return s.run(ctx, runID, symbol)
}

func (s *Service) GetRun(runID string) (contracts.RunResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, ok := s.runs[runID]
	return result, ok
}

func (s *Service) pruneRunsLocked() {
	for len(s.runOrder) > s.runLimit {
		remove := -1
		for index, runID := range s.runOrder {
			result := s.runs[runID]
			if result.Status != contracts.RunQueued && result.Status != contracts.RunRunning {
				remove = index
				break
			}
		}
		if remove < 0 {
			return
		}
		runID := s.runOrder[remove]
		delete(s.runs, runID)
		copy(s.runOrder[remove:], s.runOrder[remove+1:])
		s.runOrder[len(s.runOrder)-1] = ""
		s.runOrder = s.runOrder[:len(s.runOrder)-1]
	}
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
	if s.repository != nil {
		// Keep durable run history aligned with the bounded API history. System
		// checkpoints (risk state and runtime preferences) are excluded by the
		// repository implementation.
		_, _ = s.repository.PruneCheckpoints(ctx, s.runLimit)
	}
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
			s.freezeDurability("risk state persistence failed")
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
	existing, relation := positionForProposal(positions, proposal)
	adding := relation == positionSameSide
	if adding && existing != nil {
		trades := []riskstate.TradeRecord{}
		if s.riskState != nil {
			trades = s.riskState.Trades()
		}
		var addOn AddOnEvaluation
		proposal, addOn = evaluateAddOn(settings, proposal, *existing, mark, equity, trades, time.Now().UTC())
		result.Proposal = &proposal
		s.events.Emit("add_on_evaluated", runID, map[string]any{
			"symbol": symbol, "add_on": addOn, "proposal": proposal,
		})
		if !addOn.Allowed {
			proposal.Thesis += "（未加仓：" + addOn.Reason + "）"
			result.Proposal = &proposal
			result.Status = contracts.RunNoTrade
			return result
		}
		if err := s.checkpoint(ctx, runID, "add_on", addOn); err != nil {
			return fail(result, err)
		}
	}
	reversing := relation == positionOppositeSide

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

	riskPositions := positions
	if reversing {
		riskPositions = positionsWithout(riskPositions, existing)
	}
	view := portfolio.Build(riskPositions, s.Marks(), equity, settings.Risk.MaxCorrelatedExposure)
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
	decisionExisting := existing
	if adding {
		decisionExisting = nil
	}
	decisionResult, err := s.decide(ctx, runID, proposal, assessment, settings, equity, decisionExisting)
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
	finalAssessment, finalProposal, err := s.reassessExecutableProposal(
		ctx, runID+"-final-pf", finalProposal, riskContext, settings,
	)
	if err != nil {
		return fail(result, fmt.Errorf("final risk refresh: %w", err))
	}
	if strings.HasPrefix(decision.Operator, "auto-") && finalAssessment.RiskScore > settings.Automation.MaxRiskScore {
		finalAssessment.Verdict = contracts.VerdictRejected
		finalAssessment.HardViolations = append(finalAssessment.HardViolations,
			fmt.Sprintf("auto_risk_score: 最终风险分 %.2f > 自动审批上限 %.2f",
				finalAssessment.RiskScore, settings.Automation.MaxRiskScore))
	}
	if finalAssessment.Verdict == contracts.VerdictRejected {
		result.Assessment = &finalAssessment
		result.Proposal = &finalProposal
		result.Status = contracts.RunRejected
		s.events.Emit("risk_assessed", runID, map[string]any{"symbol": symbol, "assessment": finalAssessment})
		return result
	}
	result.Assessment = &finalAssessment
	s.events.Emit("risk_reassessed", runID, map[string]any{
		"symbol": symbol, "assessment": finalAssessment, "proposal": finalProposal,
	})
	if safetyErr := s.checkNewPosition(settings); safetyErr != nil {
		return fail(result, fmt.Errorf("execution blocked by runtime safety control: %w", safetyErr))
	}
	result.Proposal = &finalProposal
	if reversing {
		if err := s.closeForReversal(ctx, runID, existing, mark); err != nil {
			return fail(result, fmt.Errorf("reverse close: %w", err))
		}
		s.reversals.Reset(existing.Symbol, existing.Instrument)
		settings = s.control.Settings()
		if safetyErr := s.checkNewPosition(settings); safetyErr != nil {
			return fail(result, fmt.Errorf("reverse reopen blocked by runtime safety control: %w", safetyErr))
		}
		refreshed, adjusted, refreshErr := s.refreshRiskAfterReversal(ctx, finalProposal, mark, settings)
		if refreshErr != nil {
			return fail(result, fmt.Errorf("reverse reopen risk refresh: %w", refreshErr))
		}
		result.Assessment = &refreshed
		if refreshed.Verdict == contracts.VerdictRejected {
			result.Status = contracts.RunRejected
			return result
		}
		finalProposal = adjusted
		result.Proposal = &finalProposal
	}

	orderIntent := intentFor(runID, finalProposal, finalProposal.SizeQuote)
	if err := s.checkpoint(ctx, runID, "order_intent", orderIntent); err != nil {
		return fail(result, err)
	}
	if err := s.journal.OpenContext(ctx, runID+":open", orderIntent); err != nil {
		s.freezeDurability("order intent journal persistence failed")
		return fail(result, fmt.Errorf("persist order intent: %w", err))
	}
	if err := s.journal.TransitionContext(ctx, runID+":submit", orderIntent.ClientID, contracts.OrderStatusSubmitting, nil, ""); err != nil {
		s.freezeDurability("order submission journal persistence failed")
		return fail(result, fmt.Errorf("persist order submission: %w", err))
	}
	executionSpan := startSpan(ctx, "execution")
	execution, err := s.venue.Place(ctx, orderIntent)
	executionSpan.End(err)
	if err != nil {
		status := contracts.OrderStatusFailed
		if s.freezeUnknownOrder(err) {
			status = contracts.OrderStatusUnknown
		}
		journalErr := s.journal.TransitionContext(ctx, runID+":fail", orderIntent.ClientID, status, nil, err.Error())
		if journalErr != nil {
			s.freezeDurability("order failure journal persistence failed")
		}
		return fail(result, errors.Join(fmt.Errorf("execute: %w", err), journalErr))
	}
	// After an exchange acknowledgement/fill, caller cancellation must not
	// interrupt journaling, protection remediation, or risk-state persistence.
	// Bound the detached critical section so shutdown still completes.
	postExecutionContext, cancelPostExecution := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancelPostExecution()
	journalErr := s.recordExecution(postExecutionContext, runID, orderIntent, execution)
	if journalErr != nil {
		s.freezeDurability("execution result journal persistence failed")
	}
	result.Execution = &execution
	if journalErr != nil {
		return fail(result, fmt.Errorf("persist execution result: %w", journalErr))
	}
	// A full or terminalized partial entry whose protection the venue did not
	// verify must be remediated (or emergency-flattened) before any noncritical
	// checkpoint/review work may run.
	if order, tracked := s.journal.Get(orderIntent.ClientID); tracked &&
		order.Status == contracts.OrderStatusProtectiveFailed {
		if remediationErr := s.remediateProtection(postExecutionContext, runID, orderIntent, mark); remediationErr != nil {
			return fail(result, fmt.Errorf("protective remediation: %w", remediationErr))
		}
	}
	s.events.Emit("executed", runID, map[string]any{"symbol": symbol, "execution": execution})
	if reversing && executionOpenedPosition(execution) {
		s.events.Emit("reversal_opened", runID, map[string]any{
			"symbol": symbol, "side": finalProposal.Side, "execution": execution,
		})
	}
	if err := s.checkpoint(postExecutionContext, runID, "execution", execution); err != nil {
		return fail(result, err)
	}
	if s.riskState != nil && executionOpenedPosition(execution) {
		balancesAfter, balanceErr := s.venue.Balances(postExecutionContext)
		if balanceErr != nil {
			return fail(result, fmt.Errorf("post-execution balances: %w", balanceErr))
		}
		equityAfter := balancesAfter.TotalQuote
		if !equityAfter.IsPositive() {
			equityAfter = balancesAfter.FreeQuote
		}
		if err := s.riskState.RecordOpen(postExecutionContext, runID, finalProposal, execution, equityAfter); err != nil {
			s.freezeDurability("executed trade risk state persistence failed")
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
			if executionOpenedPosition(execution) {
				s.freezeDurability("executed trade review persistence failed")
			}
			return fail(result, fmt.Errorf("persist lessons: %w", err))
		}
	}
	if executionOpenedPosition(execution) {
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
	equity contracts.Decimal,
	existing *contracts.Position,
) (approval.DecisionResult, error) {
	metrics := evaluateAutoApproval(settings, proposal, assessment, equity)
	if existing != nil {
		return s.decideReversal(runID, proposal, settings, metrics, *existing), nil
	}
	if metrics.Allowed {
		proposal.SizeQuote = metrics.RecommendedQuote
		decision := contracts.ApprovalDecision{
			Decision: contracts.ApprovalApprove, Operator: "auto-policy",
			TS: time.Now().UTC(), Note: autoApprovalNote(metrics),
		}
		return approval.DecisionResult{Decision: decision, FinalProposal: proposal}, nil
	}
	if automationApprovalEnabled(settings) {
		decision := contracts.ApprovalDecision{
			Decision: contracts.ApprovalReject, Operator: "auto-policy",
			TS: time.Now().UTC(), Note: "数学自动审批拒绝：" + metrics.Reason,
		}
		return approval.DecisionResult{Decision: decision, FinalProposal: proposal}, nil
	}
	return s.gate.Decide(ctx, runID, proposal, assessment)
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
			Mode: settings.Mode, ExecutionVenue: settings.ExecutionVenue,
			ExecutionDemo: settings.OKXDemoExecutionConfigured(),
			ExecutionLive: settings.OKXLiveExecutionConfigured(), Kill: settings.Kill,
		})
	}
	if !settings.NewPositionAllowed() {
		return errors.New("only Paper, a configured OKX Demo, or an explicitly enabled OKX live account may open positions")
	}
	return nil
}

// recordExecution journals the venue outcome. The journal only accepts legal
// transitions, so a venue reporting an unexpected status leaves the order in
// its last consistent state instead of corrupting the log. For a filled entry
// that requested protection, the lifecycle fails closed: verified protective
// orders advance to protective_placed, anything else lands in
// protective_failed so remediation is forced to run.
func (s *Service) recordExecution(ctx context.Context, runID string, intent contracts.OrderIntent, execution contracts.ExecutionResult) error {
	status := execution.Status
	if !orders.CanTransition(contracts.OrderStatusSubmitting, status) {
		status = contracts.OrderStatusUnknown
	}
	if err := s.journal.TransitionContext(ctx, runID+":result", intent.ClientID, status, &execution, ""); err != nil {
		return err
	}
	if !executionOpenedPosition(execution) {
		return nil
	}
	if s.protectionSatisfied(intent, execution.FilledBase, execution.ProtectiveOrders) {
		return s.journal.TransitionContext(ctx, runID+":protective", intent.ClientID, contracts.OrderStatusProtectivePlaced, nil, "")
	}
	if intentExpectsProtection(intent) {
		return s.journal.TransitionContext(ctx, runID+":protective", intent.ClientID, contracts.OrderStatusProtectiveFailed, nil,
			"venue did not verify the requested protective orders")
	}
	return nil
}

func executionOpenedPosition(execution contracts.ExecutionResult) bool {
	return execution.FilledBase.IsPositive() &&
		(execution.Status == contracts.OrderStatusFilled || execution.Status == contracts.OrderStatusPartiallyFilled)
}

func intentExpectsProtection(intent contracts.OrderIntent) bool {
	if intent.ReduceOnly {
		return false
	}
	if intent.StopLoss != nil && intent.StopLoss.IsPositive() {
		return true
	}
	return len(intent.TakeProfit) > 0 && intent.TakeProfit[0].IsPositive()
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

// Orders returns the durable order audit view, newest activity first.
func (s *Service) Orders() []orders.Order {
	return s.journal.Orders()
}

func (s *Service) checkpoint(ctx context.Context, runID, step string, value any) error {
	if s.repository == nil {
		return nil
	}
	if err := s.repository.SaveCheckpoint(ctx, runID, step, value); err != nil {
		s.freezeDurability("durable checkpoint persistence failed")
		return fmt.Errorf("persist checkpoint %s: %w", step, err)
	}
	return nil
}

func (s *Service) freezeDurability(reason string) {
	if s.safety != nil {
		s.safety.Freeze(reason)
	}
}

func (s *Service) freezeUnknownOrder(err error) bool {
	if !errors.Is(err, venue.ErrOrderStateUnknown) {
		return false
	}
	if s.safety != nil {
		s.safety.Freeze("exchange order submission state is unknown")
	}
	return true
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
		ClientID: clientID, Symbol: proposal.Symbol, Venue: proposal.Venue,
		Side: proposal.Side, Instrument: proposal.Instrument, OrderType: proposal.Entry.Type,
		SizeQuote: size, Price: proposalEntryPrice(proposal.Entry),
		Leverage: proposal.Leverage, MarginMode: proposal.MarginMode,
		StopLoss:   proposal.StopLoss,
		TakeProfit: append(contracts.List[contracts.Decimal]{}, proposal.TakeProfit...),
	}
}

func proposalEntryPrice(plan contracts.PricePlan) *contracts.Decimal {
	if plan.Price != nil && plan.Price.IsPositive() {
		price := *plan.Price
		return &price
	}
	if plan.Low == nil || plan.High == nil || !plan.Low.IsPositive() || !plan.High.IsPositive() {
		return nil
	}
	midpoint, err := plan.Low.Add(*plan.High).Quo(contracts.NewDecimalFromInt64(2))
	if err != nil || !midpoint.IsPositive() {
		return nil
	}
	return &midpoint
}

func limitsFromConfig(value config.RiskConfig) risk.Limits {
	return risk.Limits{
		MaxRiskPerTrade:          value.MaxRiskPerTrade,
		MaxPositionPct:           value.MaxPositionPct,
		MaxGrossExposure:         value.MaxGrossExposure,
		MaxSymbolConcentration:   value.MaxSymbolConcentration,
		MaxCorrelatedExposure:    value.MaxCorrelatedExposure,
		MaxCVARPct:               value.MaxCVARPct,
		MaxOrdersPerHour:         value.MaxOrdersPerHour,
		MaxSlippageBPS:           value.MaxSlippageBPS,
		MaxLeverage:              value.MaxLeverage,
		MaxMarginPct:             value.MaxMarginPct,
		LeverageStep:             value.LeverageStep,
		MinLiquidationBuffer:     value.MinLiqBuffer,
		StopLossBufferMultiple:   value.LiqStopMultiple,
		VolatilityBufferMultiple: value.LiqVolMultiple,
		LiquidationReservePct:    value.LiqReservePct,
		ForceIsolated:            value.ForceIsolated,
		MinMarginRatio:           value.MinMarginRatio,
		MaxPriceImpact:           value.MaxPriceImpact,
		MaxGasQuote:              value.MaxGasQuote,
		MinPoolTVL:               value.MinPoolTVL,
		ContractWhitelist:        value.ContractWhitelistSet(),
		RequirePrivateMempool:    value.RequirePrivateMempool,
		DailyDrawdownLimit:       value.DailyDrawdownLimit,
		WeeklyDrawdownLimit:      value.WeeklyDrawdownLimit,
		MaxDrawdownLimit:         value.MaxDrawdownLimit,
		MaxConsecutiveLosses:     value.MaxConsecutiveLosses,
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
