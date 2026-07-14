// Package runtime coordinates reconciliation, scanning, and monitoring for
// the local Paper venue, the explicitly configured OKX Demo environment, and
// the explicitly acknowledged OKX live environment.
package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

var (
	ErrLiveExecutionDisabled = errors.New("execution target is not authorized: only Paper, OKX Demo, or explicitly enabled OKX live may execute")
	ErrKillSwitch            = errors.New("kill switch is enabled")
	ErrReconciliationFrozen  = errors.New("new positions are frozen pending successful reconciliation")
	ErrReconciliationFailed  = errors.New("startup reconciliation failed")
)

type RuntimeState struct {
	Mode           string
	ExecutionVenue string
	ExecutionDemo  bool
	ExecutionLive  bool
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
	mu                  sync.RWMutex
	frozen              bool
	reason              string
	reconcileActive     bool
	freezeGeneration    uint64
	reconcileGeneration uint64
	lastReconciled      time.Time
	now                 func() time.Time
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
	state.reconcileGeneration = state.freezeGeneration
}

func (state *SafetyState) CompleteReconcile(report ReconcileReport, reconcileErr error) error {
	if state == nil {
		return ErrReconciliationFrozen
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.reconcileActive = false
	if state.freezeGeneration != state.reconcileGeneration {
		return errors.Join(
			fmt.Errorf("%w: a newer safety freeze occurred during reconciliation", ErrReconciliationFrozen),
			reconcileErr,
		)
	}
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
	state.freezeGeneration++
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
	policy, err := ResolveModePolicy(runtime.Mode)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrLiveExecutionDisabled, err)
	}
	if err := policy.ValidateExecution(runtime.executionTarget()); err != nil {
		return err
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

// ValidateExecutionVenue is also used by monitor/reconcile paths. It permits
// the local Paper venue or an adapter that explicitly proves it is wired to
// an authenticated OKX Demo or live-trading account.
func ValidateExecutionVenue(target ReconcileVenue) error {
	if target == nil {
		return fmt.Errorf("%w: venue is nil", ErrLiveExecutionDisabled)
	}
	identity := executionTarget(venue.IdentifyExecution(target))
	paperPolicy, _ := ResolveModePolicy("paper")
	if err := paperPolicy.ValidateExecution(identity); err == nil {
		return nil
	}
	livePolicy, _ := ResolveModePolicy("live")
	return livePolicy.ValidateExecution(identity)
}

// ValidatePaperVenue is retained for callers that only have venue metadata.
func ValidatePaperVenue(kind, id string) error {
	if kind != "paper" || id != "paper" {
		return fmt.Errorf("%w: venue kind=%q id=%q", ErrLiveExecutionDisabled, kind, id)
	}
	return nil
}
