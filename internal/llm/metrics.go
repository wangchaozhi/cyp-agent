package llm

import "sync"

type MetricsSnapshot struct {
	Calls            uint64  `json:"calls"`
	Successes        uint64  `json:"successes"`
	Errors           uint64  `json:"errors"`
	Retries          uint64  `json:"retries"`
	ParseErrors      uint64  `json:"parse_errors"`
	ShortCircuits    uint64  `json:"short_circuits"`
	BudgetRejections uint64  `json:"budget_rejections"`
	Timeouts         uint64  `json:"timeouts"`
	InputTokens      uint64  `json:"input_tokens"`
	OutputTokens     uint64  `json:"output_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

type Metrics struct {
	mu       sync.RWMutex
	snapshot MetricsSnapshot
}

func NewMetrics() *Metrics { return &Metrics{} }

func (metrics *Metrics) Snapshot() MetricsSnapshot {
	if metrics == nil {
		return MetricsSnapshot{}
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	return metrics.snapshot
}

func (metrics *Metrics) update(update func(*MetricsSnapshot)) {
	if metrics == nil {
		return
	}
	metrics.mu.Lock()
	update(&metrics.snapshot)
	metrics.mu.Unlock()
}
