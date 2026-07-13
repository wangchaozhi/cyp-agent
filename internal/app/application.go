// Package app wires the modular Go backend without hiding package boundaries.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/alerts"
	"github.com/wangchaozhi/cyp-agent/internal/api"
	"github.com/wangchaozhi/cyp-agent/internal/approval"
	"github.com/wangchaozhi/cyp-agent/internal/config"
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
	state := control.New(settings)
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
	executionVenue := venue.Venue(paper)
	switch settings.ExecutionVenue {
	case "paper":
	case "okx":
		if !okx.DemoTradingEnabled() {
			_ = binance.Close()
			_ = okx.Close()
			return nil, fmt.Errorf("OKX execution requires mode=paper, CYP_OKX_DEMO=true, and complete Demo credentials")
		}
		executionVenue = okx
	default:
		_ = binance.Close()
		_ = okx.Close()
		return nil, fmt.Errorf("execution venue %q is hard-disabled; use paper or OKX Demo", settings.ExecutionVenue)
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
	runMetrics := metrics.NewRuns()
	balances, err := executionVenue.Balances(ctx)
	if err != nil {
		_ = repository.Close()
		return nil, fmt.Errorf("read initial %s balance: %w", executionVenue.ID(), err)
	}
	riskTracker, err := riskstate.New(ctx, repository, balances.TotalQuote)
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
	runtimeEngine, err := runtimecore.NewEngine(runtimecore.EngineConfig{
		Reconciler: reconciler, Scanner: scanner, Monitor: monitor,
		Safety: safety, Logger: logger, Metrics: runtimeMetrics,
	})
	if err != nil {
		orch.Close()
		_ = repository.Close()
		return nil, err
	}
	if settings.RuntimeAutostart {
		if err := runtimeEngine.Start(ctx); err != nil {
			orch.Close()
			_ = repository.Close()
			return nil, fmt.Errorf("start runtime: %w", err)
		}
	} else if _, err := runtimeEngine.StartupReconcile(ctx); err != nil {
		orch.Close()
		_ = repository.Close()
		return nil, fmt.Errorf("startup reconcile: %w", err)
	}

	server, err := api.New(api.Dependencies{
		Control: state, Venue: executionVenue, Events: bus, Gate: gate, Orchestrator: orch,
		Metrics: runMetrics, RuntimeMetrics: runtimeMetrics, Registry: registry,
		Market: aggregator, Safety: safety, HistoricalVenue: historicalVenue,
		RiskState: riskTracker,
		WebDir:    webDir, Logger: logger, APIToken: settings.APIToken.Reveal(),
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
