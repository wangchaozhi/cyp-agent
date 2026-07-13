package observability

import (
	"sync/atomic"

	compatmetrics "github.com/wangchaozhi/cyp-agent/internal/metrics"
)

// RunMetrics aliases the dashboard-compatible metrics implementation so API
// and orchestrator code have one source of truth.
type RunMetrics = compatmetrics.Runs
type RunMetricsSnapshot = compatmetrics.RunSnapshot

func NewRunMetrics() *RunMetrics { return compatmetrics.NewRuns() }

type RuntimeMetricsSnapshot struct {
	ScanCycles        uint64 `json:"scan_cycles"`
	ScanErrors        uint64 `json:"scan_errors"`
	MonitorCycles     uint64 `json:"monitor_cycles"`
	MonitorErrors     uint64 `json:"monitor_errors"`
	ReconcileAttempts uint64 `json:"reconcile_attempts"`
	ReconcileFailures uint64 `json:"reconcile_failures"`
	Alerts            uint64 `json:"alerts"`
	OHLCVQueued       uint64 `json:"ohlcv_queued"`
	OHLCVSaved        uint64 `json:"ohlcv_saved"`
	OHLCVDropped      uint64 `json:"ohlcv_dropped"`
	OHLCVErrors       uint64 `json:"ohlcv_errors"`
	OHLCVPruned       uint64 `json:"ohlcv_pruned"`
	OHLCVRepairRuns   uint64 `json:"ohlcv_repair_runs"`
	OHLCVBackfilled   uint64 `json:"ohlcv_backfilled"`
	TokenUsageQueued  uint64 `json:"token_usage_queued"`
	TokenUsageSaved   uint64 `json:"token_usage_saved"`
	TokenUsageDropped uint64 `json:"token_usage_dropped"`
	TokenUsageErrors  uint64 `json:"token_usage_errors"`
}

type RuntimeMetrics struct {
	scanCycles        atomic.Uint64
	scanErrors        atomic.Uint64
	monitorCycles     atomic.Uint64
	monitorErrors     atomic.Uint64
	reconcileAttempts atomic.Uint64
	reconcileFailures atomic.Uint64
	alerts            atomic.Uint64
	ohlcvQueued       atomic.Uint64
	ohlcvSaved        atomic.Uint64
	ohlcvDropped      atomic.Uint64
	ohlcvErrors       atomic.Uint64
	ohlcvPruned       atomic.Uint64
	ohlcvRepairRuns   atomic.Uint64
	ohlcvBackfilled   atomic.Uint64
	tokenUsageQueued  atomic.Uint64
	tokenUsageSaved   atomic.Uint64
	tokenUsageDropped atomic.Uint64
	tokenUsageErrors  atomic.Uint64
}

func (metrics *RuntimeMetrics) RecordOHLCVQueued(count uint64) {
	if metrics != nil {
		metrics.ohlcvQueued.Add(count)
	}
}

func (metrics *RuntimeMetrics) RecordOHLCVSaved(count uint64) {
	if metrics != nil {
		metrics.ohlcvSaved.Add(count)
	}
}

func (metrics *RuntimeMetrics) RecordOHLCVDropped(count uint64) {
	if metrics != nil {
		metrics.ohlcvDropped.Add(count)
	}
}

func (metrics *RuntimeMetrics) RecordOHLCVError() {
	if metrics != nil {
		metrics.ohlcvErrors.Add(1)
	}
}

func (metrics *RuntimeMetrics) RecordOHLCVPruned(count uint64) {
	if metrics != nil {
		metrics.ohlcvPruned.Add(count)
	}
}

func (metrics *RuntimeMetrics) RecordOHLCVRepairRun() {
	if metrics != nil {
		metrics.ohlcvRepairRuns.Add(1)
	}
}

func (metrics *RuntimeMetrics) RecordOHLCVBackfilled(count uint64) {
	if metrics != nil {
		metrics.ohlcvBackfilled.Add(count)
	}
}

func (metrics *RuntimeMetrics) RecordTokenUsageQueued() {
	if metrics != nil {
		metrics.tokenUsageQueued.Add(1)
	}
}

func (metrics *RuntimeMetrics) RecordTokenUsageSaved() {
	if metrics != nil {
		metrics.tokenUsageSaved.Add(1)
	}
}

func (metrics *RuntimeMetrics) RecordTokenUsageDropped() {
	if metrics != nil {
		metrics.tokenUsageDropped.Add(1)
	}
}

func (metrics *RuntimeMetrics) RecordTokenUsageError() {
	if metrics != nil {
		metrics.tokenUsageErrors.Add(1)
	}
}

func (metrics *RuntimeMetrics) RecordScan(err error) {
	if metrics == nil {
		return
	}
	metrics.scanCycles.Add(1)
	if err != nil {
		metrics.scanErrors.Add(1)
	}
}

func (metrics *RuntimeMetrics) RecordMonitor(err error) {
	if metrics == nil {
		return
	}
	metrics.monitorCycles.Add(1)
	if err != nil {
		metrics.monitorErrors.Add(1)
	}
}

func (metrics *RuntimeMetrics) RecordReconcile(err error) {
	if metrics == nil {
		return
	}
	metrics.reconcileAttempts.Add(1)
	if err != nil {
		metrics.reconcileFailures.Add(1)
	}
}

func (metrics *RuntimeMetrics) RecordAlert() {
	if metrics != nil {
		metrics.alerts.Add(1)
	}
}

func (metrics *RuntimeMetrics) Snapshot() RuntimeMetricsSnapshot {
	if metrics == nil {
		return RuntimeMetricsSnapshot{}
	}
	return RuntimeMetricsSnapshot{
		ScanCycles: metrics.scanCycles.Load(), ScanErrors: metrics.scanErrors.Load(),
		MonitorCycles: metrics.monitorCycles.Load(), MonitorErrors: metrics.monitorErrors.Load(),
		ReconcileAttempts: metrics.reconcileAttempts.Load(),
		ReconcileFailures: metrics.reconcileFailures.Load(), Alerts: metrics.alerts.Load(),
		OHLCVQueued: metrics.ohlcvQueued.Load(), OHLCVSaved: metrics.ohlcvSaved.Load(),
		OHLCVDropped: metrics.ohlcvDropped.Load(), OHLCVErrors: metrics.ohlcvErrors.Load(),
		OHLCVPruned: metrics.ohlcvPruned.Load(), OHLCVRepairRuns: metrics.ohlcvRepairRuns.Load(),
		OHLCVBackfilled:  metrics.ohlcvBackfilled.Load(),
		TokenUsageQueued: metrics.tokenUsageQueued.Load(), TokenUsageSaved: metrics.tokenUsageSaved.Load(),
		TokenUsageDropped: metrics.tokenUsageDropped.Load(), TokenUsageErrors: metrics.tokenUsageErrors.Load(),
	}
}
