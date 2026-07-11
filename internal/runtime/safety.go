// Package runtime coordinates reconciliation, scanning, and monitoring for the
// Paper-only first Go release.
package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrLiveExecutionDisabled = errors.New("live and non-paper execution are hard-disabled")
	ErrKillSwitch            = errors.New("kill switch is enabled")
	ErrReconciliationFrozen  = errors.New("new positions are frozen pending successful reconciliation")
	ErrReconciliationFailed  = errors.New("startup reconciliation failed")
)

type RuntimeState struct {
	Mode           string
	ExecutionVenue string
	Kill           bool
}

type SafetySnapshot struct {
	Frozen          bool      `json:"frozen"`
	Reason          string    `json:"reason"`
	LastReconciled  time.Time `json:"last_reconciled,omitempty"`
	ReconcileActive bool      `json:"reconcile_active"`
}

// SafetyState starts frozen. There is deliberately no generic Unfreeze method:
// only CompleteReconcile can authorize new Paper positions.
type SafetyState struct {
	mu              sync.RWMutex
	frozen          bool
	reason          string
	reconcileActive bool
	lastReconciled  time.Time
	now             func() time.Time
}

func NewSafetyState() *SafetyState {
	return &SafetyState{
		frozen: true, reason: "startup reconciliation required", now: time.Now,
	}
}

func (state *SafetyState) BeginReconcile() {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.frozen = true
	state.reason = "reconciliation in progress"
	state.reconcileActive = true
}

func (state *SafetyState) CompleteReconcile(report ReconcileReport, reconcileErr error) error {
	if state == nil {
		return ErrReconciliationFrozen
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.reconcileActive = false
	if reconcileErr != nil {
		state.frozen = true
		state.reason = "reconciliation failed"
		return errors.Join(ErrReconciliationFailed, reconcileErr)
	}
	if !report.OK {
		state.frozen = true
		state.reason = "reconciliation reported unresolved differences"
		return ErrReconciliationFailed
	}
	state.frozen = false
	state.reason = ""
	state.lastReconciled = state.now().UTC()
	return nil
}

func (state *SafetyState) Freeze(reason string) {
	if state == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "manually frozen"
	}
	state.mu.Lock()
	state.frozen = true
	state.reason = reason
	state.mu.Unlock()
}

func (state *SafetyState) Snapshot() SafetySnapshot {
	if state == nil {
		return SafetySnapshot{Frozen: true, Reason: "safety state is unavailable"}
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return SafetySnapshot{
		Frozen: state.frozen, Reason: state.reason, LastReconciled: state.lastReconciled,
		ReconcileActive: state.reconcileActive,
	}
}

func (state *SafetyState) CheckNewPosition(runtime RuntimeState) error {
	if runtime.Mode != "paper" || runtime.ExecutionVenue != "paper" {
		return fmt.Errorf(
			"%w: mode=%q execution_venue=%q",
			ErrLiveExecutionDisabled,
			runtime.Mode,
			runtime.ExecutionVenue,
		)
	}
	if runtime.Kill {
		return ErrKillSwitch
	}
	snapshot := state.Snapshot()
	if snapshot.Frozen {
		return fmt.Errorf("%w: %s", ErrReconciliationFrozen, snapshot.Reason)
	}
	return nil
}

// ValidatePaperVenue is also used by monitor/reconcile paths, which do not
// open positions but must never connect the first Go runtime to a live venue.
func ValidatePaperVenue(kind, id string) error {
	if kind != "paper" || id != "paper" {
		return fmt.Errorf("%w: venue kind=%q id=%q", ErrLiveExecutionDisabled, kind, id)
	}
	return nil
}
