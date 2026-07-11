package runtime

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/wangchaozhi/cyp-agent/internal/observability"
)

var ErrRuntimeStarted = errors.New("runtime is already started or starting")

type loop interface {
	Run(ctx context.Context) error
}

type EngineConfig struct {
	Reconciler Reconciler
	Scanner    *Scanner
	Monitor    *PositionMonitor
	Safety     *SafetyState
	Logger     *slog.Logger
	Metrics    *observability.RuntimeMetrics
}

type Engine struct {
	reconciler Reconciler
	scanner    *Scanner
	monitor    *PositionMonitor
	safety     *SafetyState
	logger     *slog.Logger
	metrics    *observability.RuntimeMetrics

	mu        sync.Mutex
	starting  bool
	started   bool
	cancel    context.CancelFunc
	startDone chan struct{}
	wait      sync.WaitGroup
	errors    chan error
}

func NewEngine(config EngineConfig) (*Engine, error) {
	if config.Reconciler == nil {
		return nil, errors.New("runtime reconciler is required")
	}
	if config.Safety == nil {
		config.Safety = NewSafetyState()
	}
	if config.Logger == nil {
		config.Logger = observability.DefaultLogger("runtime")
	}
	if config.Scanner != nil {
		config.Scanner.safety = config.Safety
	}
	return &Engine{
		reconciler: config.Reconciler, scanner: config.Scanner, monitor: config.Monitor,
		safety: config.Safety, logger: config.Logger, metrics: config.Metrics,
		errors: make(chan error, 16),
	}, nil
}

func (engine *Engine) Safety() *SafetyState { return engine.safety }
func (engine *Engine) Errors() <-chan error { return engine.errors }

func (engine *Engine) StartupReconcile(ctx context.Context) (ReconcileReport, error) {
	engine.safety.BeginReconcile()
	report, reconcileErr := engine.reconciler.Reconcile(ctx)
	if reconcileErr == nil && !report.OK {
		reconcileErr = ErrReconciliationFailed
	}
	engine.metrics.RecordReconcile(reconcileErr)
	if err := engine.safety.CompleteReconcile(report, reconcileErr); err != nil {
		engine.logger.ErrorContext(ctx, "startup_reconcile_failed", "error", err.Error())
		return report, err
	}
	engine.logger.InfoContext(ctx, "startup_reconciled", "positions", len(report.Positions))
	return report, nil
}

func (engine *Engine) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("runtime context is required")
	}
	engine.mu.Lock()
	if engine.started || engine.starting {
		engine.mu.Unlock()
		return ErrRuntimeStarted
	}
	runContext, cancel := context.WithCancel(ctx)
	startDone := make(chan struct{})
	engine.starting = true
	engine.cancel = cancel
	engine.startDone = startDone
	engine.mu.Unlock()
	defer close(startDone)

	if _, err := engine.StartupReconcile(runContext); err != nil {
		cancel()
		engine.mu.Lock()
		engine.starting = false
		engine.cancel = nil
		engine.mu.Unlock()
		return err
	}
	if err := runContext.Err(); err != nil {
		cancel()
		engine.safety.Freeze("runtime start canceled")
		engine.mu.Lock()
		engine.starting = false
		engine.cancel = nil
		engine.mu.Unlock()
		return err
	}

	loops := make([]loop, 0, 2)
	if engine.scanner != nil {
		loops = append(loops, engine.scanner)
	}
	if engine.monitor != nil {
		loops = append(loops, engine.monitor)
	}
	engine.mu.Lock()
	engine.started = true
	engine.starting = false
	engine.mu.Unlock()
	for _, worker := range loops {
		worker := worker
		engine.wait.Add(1)
		go func() {
			defer engine.wait.Done()
			if err := worker.Run(runContext); err != nil && !errors.Is(err, context.Canceled) {
				engine.reportError(err)
			}
		}()
	}
	engine.logger.InfoContext(ctx, "runtime_started")
	return nil
}

func (engine *Engine) reportError(err error) {
	engine.logger.Error("runtime_loop_stopped", "error", err.Error())
	select {
	case engine.errors <- err:
	default:
	}
}

func (engine *Engine) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("stop context is required")
	}
	engine.mu.Lock()
	cancel := engine.cancel
	startDone := engine.startDone
	wasStarted := engine.started || engine.starting
	engine.cancel = nil
	engine.mu.Unlock()
	if !wasStarted {
		return nil
	}
	if cancel != nil {
		cancel()
	}
	if startDone != nil {
		select {
		case <-startDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan struct{})
	go func() {
		engine.wait.Wait()
		close(done)
	}()
	select {
	case <-done:
		engine.mu.Lock()
		engine.started = false
		engine.starting = false
		engine.mu.Unlock()
		engine.logger.InfoContext(ctx, "runtime_stopped")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (engine *Engine) RunBounded(ctx context.Context, scanCycles, monitorCycles int) (ReconcileReport, error) {
	report, err := engine.StartupReconcile(ctx)
	if err != nil {
		return report, err
	}
	errorsSeen := make([]error, 0, 2)
	if engine.scanner != nil {
		errorsSeen = append(errorsSeen, engine.scanner.RunCycles(ctx, scanCycles))
	}
	if engine.monitor != nil {
		errorsSeen = append(errorsSeen, engine.monitor.RunCycles(ctx, monitorCycles))
	}
	return report, observability.JoinErrors(errorsSeen...)
}

func (engine *Engine) Started() bool {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.started
}
