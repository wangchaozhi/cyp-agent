// Package metrics contains the in-process metrics snapshot consumed by the
// dashboard. Prometheus export can be layered on later without changing this
// compatibility contract.
package metrics

import (
	"math"
	"sync"
	"time"
)

// ApprovalLatency is kept numeric because the React dashboard calls toFixed.
type ApprovalLatency struct {
	AverageSeconds float64 `json:"avg_s"`
	MaxSeconds     float64 `json:"max_s"`
	Count          int     `json:"n"`
}

// RunSnapshot defines the stable /api/metrics payload.
type RunSnapshot struct {
	Runs               int             `json:"runs"`
	Executed           int             `json:"executed"`
	Rejected           int             `json:"rejected"`
	NotApproved        int             `json:"not_approved"`
	NoTrade            int             `json:"no_trade"`
	Errors             int             `json:"errors"`
	AverageSlippageBPS float64         `json:"avg_slippage_bps"`
	ApprovalRate       float64         `json:"approval_rate"`
	OrderSuccessRate   float64         `json:"order_success_rate"`
	SlippageHistogram  map[string]int  `json:"slippage_hist_bps"`
	ApprovalLatency    ApprovalLatency `json:"approval_latency"`
}

// Runs is safe for concurrent HTTP handlers and orchestrator goroutines.
type Runs struct {
	mu sync.RWMutex

	runs        int
	executed    int
	rejected    int
	notApproved int
	noTrade     int
	errors      int

	slippageSum float64
	slippageN   int
	histogram   map[string]int

	approvalSum time.Duration
	approvalMax time.Duration
	approvalN   int
}

func NewRuns() *Runs {
	return &Runs{histogram: map[string]int{"0-5": 0, "5-15": 0, "15-30": 0, "30+": 0}}
}

// Record records a terminal run status and optional execution slippage.
func (m *Runs) Record(status string, slippageBPS *float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs++
	switch status {
	case "executed":
		m.executed++
	case "rejected":
		m.rejected++
	case "not_approved":
		m.notApproved++
	case "no_trade":
		m.noTrade++
	default:
		m.errors++
	}
	if slippageBPS == nil || math.IsNaN(*slippageBPS) || math.IsInf(*slippageBPS, 0) {
		return
	}
	m.slippageSum += *slippageBPS
	m.slippageN++
	switch {
	case *slippageBPS < 5:
		m.histogram["0-5"]++
	case *slippageBPS < 15:
		m.histogram["5-15"]++
	case *slippageBPS < 30:
		m.histogram["15-30"]++
	default:
		m.histogram["30+"]++
	}
}

func (m *Runs) RecordApprovalLatency(duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvalSum += duration
	if duration > m.approvalMax {
		m.approvalMax = duration
	}
	m.approvalN++
}

func (m *Runs) Snapshot() RunSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	avgSlippage := 0.0
	if m.slippageN > 0 {
		avgSlippage = m.slippageSum / float64(m.slippageN)
	}
	approvalRate := 0.0
	if decided := m.executed + m.notApproved; decided > 0 {
		approvalRate = float64(m.executed) / float64(decided)
	}
	orderSuccessRate := 0.0
	if attempted := m.executed + m.errors; attempted > 0 {
		orderSuccessRate = float64(m.executed) / float64(attempted)
	}
	avgApproval := 0.0
	if m.approvalN > 0 {
		avgApproval = m.approvalSum.Seconds() / float64(m.approvalN)
	}
	histogram := make(map[string]int, len(m.histogram))
	for key, value := range m.histogram {
		histogram[key] = value
	}

	return RunSnapshot{
		Runs: m.runs, Executed: m.executed, Rejected: m.rejected,
		NotApproved: m.notApproved, NoTrade: m.noTrade, Errors: m.errors,
		AverageSlippageBPS: round(avgSlippage, 2),
		ApprovalRate:       round(approvalRate, 3),
		OrderSuccessRate:   round(orderSuccessRate, 3),
		SlippageHistogram:  histogram,
		ApprovalLatency: ApprovalLatency{
			AverageSeconds: round(avgApproval, 3),
			MaxSeconds:     round(m.approvalMax.Seconds(), 3),
			Count:          m.approvalN,
		},
	}
}

func round(value float64, precision int) float64 {
	power := math.Pow10(precision)
	return math.Round(value*power) / power
}
