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
}

type RuntimeMetrics struct {
	scanCycles        atomic.Uint64
	scanErrors        atomic.Uint64
	monitorCycles     atomic.Uint64
	monitorErrors     atomic.Uint64
	reconcileAttempts atomic.Uint64
	reconcileFailures atomic.Uint64
	alerts            atomic.Uint64
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
	}
}
