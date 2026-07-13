package orchestrator

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/riskstate"
)

// ReversalDecision is the auditable output of the anti-whipsaw state machine.
// Confirmations are deliberately in-memory: a restart requires the signal to
// prove itself again, while completed reversals remain durable in risk trades.
type ReversalDecision struct {
	Ready          bool           `json:"ready"`
	Side           contracts.Side `json:"side"`
	Confirmations  int            `json:"confirmations"`
	Required       int            `json:"required"`
	ReversalsToday int            `json:"reversals_today"`
	CooldownUntil  *time.Time     `json:"cooldown_until,omitempty"`
	Reason         string         `json:"reason"`
}

type reversalSignal struct {
	side contracts.Side
	seen int
	last time.Time
}

type ReversalTracker struct {
	mu      sync.Mutex
	signals map[string]reversalSignal
}

func NewReversalTracker() *ReversalTracker {
	return &ReversalTracker{signals: make(map[string]reversalSignal)}
}

func (tracker *ReversalTracker) Observe(
	position contracts.Position,
	proposal contracts.TradeProposal,
	now time.Time,
	settings config.AutomationConfig,
	trades []riskstate.TradeRecord,
) ReversalDecision {
	decision := ReversalDecision{Side: proposal.Side, Required: settings.ReverseConfirmations}
	if tracker == nil {
		decision.Reason = "反向确认器不可用"
		return decision
	}
	key := reversalKey(position.Symbol, position.Instrument)
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if !settings.Enabled || !settings.EntryEnabled || !settings.ApprovalEnabled || !settings.ReverseEnabled {
		delete(tracker.signals, key)
		decision.Reason = "自动反向已关闭"
		return decision
	}
	if position.Side == proposal.Side || proposal.Side == contracts.SideFlat {
		delete(tracker.signals, key)
		decision.Reason = "没有相反方向信号"
		return decision
	}
	now = now.UTC()
	lastReversal, count := completedReversalsToday(trades, position.Symbol, position.Instrument, now)
	decision.ReversalsToday = count
	if count >= settings.MaxReversalsPerDay {
		delete(tracker.signals, key)
		decision.Reason = "已达到每日自动反向次数上限"
		return decision
	}
	if !lastReversal.IsZero() && settings.ReverseCooldownMins > 0 {
		cooldownUntil := lastReversal.Add(time.Duration(settings.ReverseCooldownMins) * time.Minute)
		if now.Before(cooldownUntil) {
			delete(tracker.signals, key)
			decision.CooldownUntil = &cooldownUntil
			decision.Reason = "自动反向仍在冷却期"
			return decision
		}
	}
	current := tracker.signals[key]
	window := time.Duration(settings.ReverseSignalMinutes) * time.Minute
	if current.side != proposal.Side || current.last.IsZero() || now.Sub(current.last) > window || now.Before(current.last) {
		current = reversalSignal{side: proposal.Side, seen: 1, last: now}
	} else {
		current.seen++
		current.last = now
	}
	tracker.signals[key] = current
	decision.Confirmations = current.seen
	if current.seen < settings.ReverseConfirmations {
		decision.Reason = fmt.Sprintf("反向信号确认中（%d/%d）", current.seen, settings.ReverseConfirmations)
		return decision
	}
	decision.Ready = true
	decision.Reason = "反向信号连续确认通过"
	return decision
}

func (tracker *ReversalTracker) Reset(symbol string, instrument contracts.Instrument) {
	if tracker == nil {
		return
	}
	tracker.mu.Lock()
	delete(tracker.signals, reversalKey(symbol, instrument))
	tracker.mu.Unlock()
}

func reversalKey(symbol string, instrument contracts.Instrument) string {
	return strings.ToUpper(strings.TrimSpace(symbol)) + "\x00" + string(instrument)
}

func completedReversalsToday(
	trades []riskstate.TradeRecord,
	symbol string,
	instrument contracts.Instrument,
	now time.Time,
) (time.Time, int) {
	day := now.UTC().Format("2006-01-02")
	var last time.Time
	count := 0
	for _, trade := range trades {
		if trade.Kind != "close" || trade.Symbol != symbol || trade.Instrument != instrument ||
			!strings.HasPrefix(trade.ClientID, "reverse-close-") || trade.TS.UTC().Format("2006-01-02") != day {
			continue
		}
		count++
		if trade.TS.After(last) {
			last = trade.TS.UTC()
		}
	}
	return last, count
}
