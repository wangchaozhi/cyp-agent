// Package app wires the modular Go backend without hiding package boundaries.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/alerts"
	"github.com/wangchaozhi/cyp-agent/internal/api"
	"github.com/wangchaozhi/cyp-agent/internal/approval"
	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/control"
	"github.com/wangchaozhi/cyp-agent/internal/data"
	"github.com/wangchaozhi/cyp-agent/internal/events"
	"github.com/wangchaozhi/cyp-agent/internal/llm"
	"github.com/wangchaozhi/cyp-agent/internal/metrics"
	"github.com/wangchaozhi/cyp-agent/internal/observability"
	"github.com/wangchaozhi/cyp-agent/internal/orchestrator"
	"github.com/wangchaozhi/cyp-agent/internal/persistence"
	"github.com/wangchaozhi/cyp-agent/internal/riskstate"
	runtimecore "github.com/wangchaozhi/cyp-agent/internal/runtime"
	"github.com/wangchaozhi/cyp-agent/internal/runtimeprefs"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

type Application struct {
	Control        *control.State
	Events         *events.Bus
	Gate           *approval.PendingGate
	Venue          venue.Venue
	Registry       *venue.VenueRegistry
	DataSource     data.Source
	Market         *data.MarketAggregator
	Repository     persistence.Repository
	Metrics        *metrics.Runs
	RuntimeMetrics *observability.RuntimeMetrics
	Safety         *runtimecore.SafetyState
	RiskState      *riskstate.Tracker
	Runtime        *runtimecore.Engine
	Orchestrator   *orchestrator.Service
	API            *api.Server

	closeOnce sync.Once
}

type buildOptions struct {
	dataSource data.Source
	repository persistence.Repository
	market     *data.MarketAggregator
}

type Option func(*buildOptions)

func WithDataSource(source data.Source) Option {
	return func(options *buildOptions) { options.dataSource = source }
}

func WithRepository(repository persistence.Repository) Option {
	return func(options *buildOptions) { options.repository = repository }
}

func WithMarketAggregator(aggregator *data.MarketAggregator) Option {
	return func(options *buildOptions) { options.market = aggregator }
}

func New(
	ctx context.Context,
	settings config.Settings,
	webDir string,
	logger *slog.Logger,
	options ...Option,
) (*Application, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	configured := buildOptions{}
	for _, option := range options {
		option(&configured)
	}
	bus := events.NewBus(1000)
	paper := venue.NewPaperVenue()

	binance, err := venue.NewBinanceVenue(venue.CEXConfig{
		APIKey: settings.BinanceAPIKey.Reveal(), APISecret: settings.BinanceAPISecret.Reveal(),
	})
	if err != nil {
		return nil, fmt.Errorf("build Binance venue: %w", err)
	}
	okx, err := venue.NewOKXVenue(venue.CEXConfig{
		APIKey: settings.OKXAPIKey.Reveal(), APISecret: settings.OKXAPISecret.Reveal(),
		Passphrase: settings.OKXPassword.Reveal(), Demo: settings.OKXDemo,
		EnableDemoTrading: settings.OKXDemoExecutionConfigured(),
	})
	if err != nil {
		_ = binance.Close()
		return nil, fmt.Errorf("build OKX venue: %w", err)
	}
	registry := venue.NewVenueRegistry(paper, binance, okx)
	historicalVenue, _ := registry.Get(settings.CEXID)
	executionVenue, executionVenueFound := registry.Get(settings.ExecutionVenue)
	if !executionVenueFound {
		_ = binance.Close()
		_ = okx.Close()
		return nil, fmt.Errorf("execution venue %q is unavailable", settings.ExecutionVenue)
	}
	modePolicy, err := runtimecore.ResolveModePolicy(settings.Mode)
	if err != nil {
		_ = binance.Close()
		_ = okx.Close()
		return nil, err
	}
	executionIdentity := venue.IdentifyExecution(executionVenue)
	if err := modePolicy.ValidateExecution(runtimecore.ExecutionTarget{
		VenueID: executionIdentity.VenueID, Kind: executionIdentity.Kind,
		Environment: executionIdentity.Environment, Writable: executionIdentity.Writable,
	}); err != nil {
		_ = binance.Close()
		_ = okx.Close()
		return nil, fmt.Errorf("execution mode policy rejected configuration: %w", err)
	}

	source := configured.dataSource
	if source == nil {
		if settings.DataSource == "cex" {
			selected, ok := registry.Get(settings.CEXID)
			if !ok {
				return nil, fmt.Errorf("configured data venue %q is unavailable", settings.CEXID)
			}
			source, err = data.BuildSource("cex", selected)
			if err != nil {
				return nil, err
			}
		} else {
			source = data.NewSyntheticMarketData(data.WithLiveTicks(true))
		}
	}
	if api := strings.TrimSpace(settings.OnchainDataAPI); api != "" {
		fetcher, fetchErr := data.NewHTTPOnchainFetcher(api, nil)
		if fetchErr != nil {
			_ = binance.Close()
			_ = okx.Close()
			return nil, fmt.Errorf("configure onchain data API: %w", fetchErr)
		}
		source, err = data.NewOnchainEnrichedSource(source, data.NewOnchainDataSource(fetcher))
		if err != nil {
			_ = binance.Close()
			_ = okx.Close()
			return nil, err
		}
	}
	aggregator := configured.market
	if aggregator == nil && settings.DataSource == "cex" {
		aggregator = data.NewMarketAggregator([]data.TickerVenue{binance, okx})
	}

	repository := configured.repository
	if repository == nil {
		repository, err = buildRepository(ctx, settings)
		if err != nil {
			_ = binance.Close()
			_ = okx.Close()
			return nil, err
		}
	}
	preferenceStore := runtimeprefs.New(repository)
	state := control.New(settings)
	if savedWatchlist, found, loadErr := preferenceStore.LoadWatchlist(ctx); loadErr != nil {
		_ = repository.Close()
		return nil, fmt.Errorf("restore runtime watchlist: %w", loadErr)
	} else if found {
		if updateErr := state.UpdateSettings(contracts.SettingsUpdateRequest{Watchlist: &savedWatchlist}); updateErr != nil {
			_ = repository.Close()
			return nil, fmt.Errorf("restore runtime watchlist: %w", updateErr)
		}
		settings = state.Settings()
	}
	if savedAutomation, found, loadErr := preferenceStore.LoadAutomation(ctx); loadErr != nil {
		_ = repository.Close()
		return nil, fmt.Errorf("restore runtime automation: %w", loadErr)
	} else if found {
		settings = state.Settings()
		settings.Automation = savedAutomation
		if validationErr := settings.Validate(); validationErr != nil {
			_ = repository.Close()
			return nil, fmt.Errorf("restore runtime automation: %w", validationErr)
		}
		state = control.New(settings)
	}
	runMetrics := metrics.NewRuns()
	balances, err := executionVenue.Balances(ctx)
	if err != nil {
		_ = repository.Close()
		return nil, fmt.Errorf("read initial %s balance: %w", executionVenue.ID(), err)
	}
	riskScope := modePolicy.RiskStateScope(runtimecore.ExecutionTarget{
		VenueID: executionIdentity.VenueID, Kind: executionIdentity.Kind,
		Environment: executionIdentity.Environment, Writable: executionIdentity.Writable,
	})
	riskTracker, err := riskstate.NewScoped(ctx, repository, balances.TotalQuote, riskScope)
	if err != nil {
		_ = repository.Close()
		return nil, fmt.Errorf("restore risk state: %w", err)
	}
	runtimeMetrics := &observability.RuntimeMetrics{}
	timeout := time.Duration(settings.Risk.ApprovalTimeoutSeconds) * time.Second
	gate := approval.NewPendingGate(timeout, bus)
	safety := runtimecore.NewSafetyState()
	baseLLM := llm.FromSettings(settings)
	// Scanner, manual API runs, and the orchestrator itself serialize on this
	// single per-symbol lock instance instead of maintaining separate maps.
	locks := runtimecore.NewSymbolLocks()
	orch := orchestrator.New(ctx, state, executionVenue, bus, gate, runMetrics,
		orchestrator.WithDataSource(source), orchestrator.WithRepository(repository),
		orchestrator.WithRiskState(riskTracker), orchestrator.WithSafety(safety), orchestrator.WithLLM(baseLLM),
		orchestrator.WithSymbolLocks(locks))

	reconciler, err := runtimecore.NewVenueReconciler(executionVenue, bus, logger)
	if err != nil {
		orch.Close()
		_ = repository.Close()
		return nil, err
	}
	alerter, err := alerts.Build(logger, runtimeMetrics, settings.AlertWebhook)
	if err != nil {
		orch.Close()
		_ = repository.Close()
		return nil, err
	}
	scanner, err := runtimecore.NewScanner(runtimecore.ScannerConfig{
		Symbols: settings.WatchlistSymbols(), Interval: time.Duration(settings.ScanInterval) * time.Second,
		SymbolProvider: func() []string { return state.Settings().WatchlistSymbols() },
		Run: func(_ context.Context, symbol string) error {
			_, startErr := orch.Start(symbol)
			return startErr
		},
		State: func() runtimecore.RuntimeState {
			current := state.Settings()
			return runtimecore.RuntimeState{
				Mode: current.Mode, ExecutionVenue: current.ExecutionVenue,
				ExecutionDemo: current.OKXDemoExecutionConfigured(), Kill: current.Kill,
			}
		},
		Safety: safety, Locks: locks, Logger: logger, Metrics: runtimeMetrics,
		Enabled: func() bool {
			automation := state.Settings().Automation
			return automation.Enabled && automation.ScanEnabled
		},
	})
	if err != nil {
		orch.Close()
		_ = repository.Close()
		return nil, err
	}
	monitor, err := runtimecore.NewPositionMonitor(runtimecore.PositionMonitorConfig{
		Venue: executionVenue, Interval: time.Duration(settings.MonitorInterval) * time.Second,
		Events: bus, Alerter: alerter, Logger: logger, Metrics: runtimeMetrics,
		MinMarginRatio: settings.Risk.MinMarginRatio,
	})
	if err != nil {
		orch.Close()
		_ = repository.Close()
		return nil, err
	}
	exitManager, err := runtimecore.NewAutomatedExitManager(runtimecore.AutomatedExitConfig{
		Venue: executionVenue, Interval: time.Duration(settings.MonitorInterval) * time.Second,
		Automation: func() config.AutomationConfig { return state.Settings().Automation },
		State: func() runtimecore.RuntimeState {
			current := state.Settings()
			return runtimecore.RuntimeState{
				Mode: current.Mode, ExecutionVenue: current.ExecutionVenue,
				ExecutionDemo: current.OKXDemoExecutionConfigured(), Kill: current.Kill,
			}
		},
		OpenedAt: func(position contracts.Position) (time.Time, bool) {
			opened, ok := riskTracker.OpenTrade(position.Symbol, position.Instrument)
			return opened.TS, ok
		},
		Exit: func(exitContext context.Context, position contracts.Position, mark contracts.Decimal, decision runtimecore.ExitDecision) error {
			return executeAutomatedExit(exitContext, executionVenue, riskTracker, orch, bus, logger, position, mark, decision)
		},
		Events: bus, Logger: logger,
	})
	if err != nil {
		orch.Close()
		_ = repository.Close()
		return nil, err
	}
	runtimeEngine, err := runtimecore.NewEngine(runtimecore.EngineConfig{
		Reconciler: reconciler, Scanner: scanner, Monitor: monitor, ExitManager: exitManager,
		Safety: safety, Logger: logger, Metrics: runtimeMetrics, AllowDegradedStart: true,
	})
	if err != nil {
		orch.Close()
		_ = repository.Close()
		return nil, err
	}
	if settings.RuntimeAutostart || settings.Automation.Enabled {
		if err := runtimeEngine.Start(ctx); err != nil {
			orch.Close()
			_ = repository.Close()
			return nil, fmt.Errorf("start runtime: %w", err)
		}
	} else if _, err := runtimeEngine.StartupReconcile(ctx); err != nil {
		// Keep the API and reduce-only controls available while SafetyState
		// remains frozen. A reconciliation gap must block new positions, not
		// make an already-open account impossible to inspect or close.
		logger.Warn("startup_reconcile_degraded", "error", err.Error(), "new_positions_frozen", true)
	}

	server, err := api.New(api.Dependencies{
		Control: state, Venue: executionVenue, Events: bus, Gate: gate, Orchestrator: orch,
		Metrics: runMetrics, RuntimeMetrics: runtimeMetrics, Registry: registry,
		Market: aggregator, Safety: safety, HistoricalVenue: historicalVenue,
		RiskState: riskTracker, WatchlistStore: preferenceStore, AutomationStore: preferenceStore,
		EnsureRuntime: func() error {
			if runtimeEngine.Started() {
				return nil
			}
			startErr := runtimeEngine.Start(ctx)
			if errors.Is(startErr, runtimecore.ErrRuntimeStarted) {
				return nil
			}
			return startErr
		},
		WebDir: webDir, Logger: logger, APIToken: settings.APIToken.Reveal(),
	})
	if err != nil {
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runtimeEngine.Stop(stopContext)
		orch.Close()
		bus.Close()
		_ = repository.Close()
		return nil, err
	}
	return &Application{
		Control: state, Events: bus, Gate: gate, Venue: executionVenue, Registry: registry,
		DataSource: source, Market: aggregator, Repository: repository,
		Metrics: runMetrics, RuntimeMetrics: runtimeMetrics, Safety: safety,
		RiskState: riskTracker,
		Runtime:   runtimeEngine, Orchestrator: orch, API: server,
	}, nil
}

func buildRepository(ctx context.Context, settings config.Settings) (persistence.Repository, error) {
	switch settings.Persistence {
	case "memory":
		return persistence.NewMemoryRepository(200), nil
	case "postgres":
		connectContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		repository, err := persistence.NewPostgresRepository(connectContext, settings.DBURL, 200)
		if err != nil {
			return nil, fmt.Errorf("open PostgreSQL persistence: %w", err)
		}
		return repository, nil
	default:
		repository, err := persistence.NewFileRepository(settings.StateFile, 200)
		if err != nil {
			return nil, fmt.Errorf("open file persistence: %w", err)
		}
		return repository, nil
	}
}

func executeAutomatedExit(
	ctx context.Context,
	target venue.Venue,
	riskTracker *riskstate.Tracker,
	orch *orchestrator.Service,
	bus *events.Bus,
	logger *slog.Logger,
	position contracts.Position,
	mark contracts.Decimal,
	decision runtimecore.ExitDecision,
) error {
	marginMode := contracts.MarginModeIsolated
	if position.MarginMode != nil {
		marginMode = *position.MarginMode
	}
	result, err := target.Place(ctx, contracts.OrderIntent{
		ClientID: fmt.Sprintf("auto-exit-%d", time.Now().UTC().UnixNano()),
		Symbol:   position.Symbol, Venue: target.ID(), Side: position.Side,
		Instrument: position.Instrument, OrderType: contracts.EntryTypeMarket,
		SizeQuote: position.SizeBase.Mul(mark), Price: &mark, Leverage: position.Leverage,
		MarginMode: marginMode, ReduceOnly: true, TakeProfit: contracts.List[contracts.Decimal]{},
	})
	if err != nil {
		return err
	}
	if result.Status != contracts.OrderStatusFilled {
		if result.Error != nil && *result.Error != "" {
			return errors.New(*result.Error)
		}
		return fmt.Errorf("automated exit status is %s", result.Status)
	}
	reference := result.ClientID
	if opened, ok := riskTracker.OpenTrade(position.Symbol, position.Instrument); ok && opened.RunID != "" {
		reference = opened.RunID
	}
	balances, balanceErr := target.Balances(ctx)
	if balanceErr != nil {
		logger.ErrorContext(ctx, "automated_exit_balance_failed", "error", balanceErr.Error())
	} else {
		equity := balances.TotalQuote
		if !equity.IsPositive() {
			equity = balances.FreeQuote
		}
		record, stateErr := riskTracker.RecordClose(ctx, reference, position, result, equity)
		if stateErr != nil {
			logger.ErrorContext(ctx, "automated_exit_persist_failed", "error", stateErr.Error())
		} else if _, reviewErr := orch.ReviewClosed(ctx, position, result, record.PNLQuote, reference); reviewErr != nil {
			logger.ErrorContext(ctx, "automated_exit_review_failed", "error", reviewErr.Error())
		}
	}
	bus.Emit("automated_exit", reference, map[string]any{
		"symbol": position.Symbol, "execution": result, "exit_decision": decision,
	})
	return nil
}

func (application *Application) Close() {
	if application == nil {
		return
	}
	application.closeOnce.Do(func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = application.Runtime.Stop(stopContext)
		application.Orchestrator.Close()
		application.Events.Close()
		_ = application.Repository.Close()
		for _, current := range application.Registry.All() {
			if closer, ok := current.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
	})
}
