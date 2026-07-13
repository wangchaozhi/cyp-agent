// Package orders implements the G4 persistent order state machine. It owns
// the legal OrderStatus transitions and an append-only, idempotently
// replayable event journal. Every writable Paper/OKX Demo path is wired here;
// real execution remains disabled and must pass the G4 acceptance checklist.
package orders

import (
	"fmt"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

// transitions lists every legal state change. Reconciliation is the only
// caller allowed to resolve OrderStatusUnknown, which is why unknown fans out
// to nearly every state: after a timeout the real venue may have done
// anything, and blind retries are forbidden.
var transitions = map[contracts.OrderStatus][]contracts.OrderStatus{
	contracts.OrderStatusNew: {
		contracts.OrderStatusPreflight, contracts.OrderStatusSubmitting,
		contracts.OrderStatusCanceled, contracts.OrderStatusRejected,
	},
	contracts.OrderStatusPreflight: {
		contracts.OrderStatusSubmitting, contracts.OrderStatusRejected, contracts.OrderStatusCanceled,
	},
	contracts.OrderStatusSubmitting: {
		contracts.OrderStatusAcknowledged, contracts.OrderStatusPartiallyFilled,
		contracts.OrderStatusFilled, contracts.OrderStatusCanceled, contracts.OrderStatusRejected,
		contracts.OrderStatusFailed, contracts.OrderStatusUnknown,
	},
	contracts.OrderStatusAcknowledged: {
		contracts.OrderStatusPartiallyFilled, contracts.OrderStatusFilled,
		contracts.OrderStatusCanceled, contracts.OrderStatusFailed, contracts.OrderStatusUnknown,
	},
	contracts.OrderStatusPartiallyFilled: {
		contracts.OrderStatusPartiallyFilled, contracts.OrderStatusFilled,
		contracts.OrderStatusCanceled, contracts.OrderStatusUnknown,
	},
	contracts.OrderStatusFilled: {
		contracts.OrderStatusProtectivePlaced, contracts.OrderStatusProtectiveFailed,
		contracts.OrderStatusFlattening, contracts.OrderStatusClosed,
	},
	contracts.OrderStatusProtectivePlaced: {
		contracts.OrderStatusFlattening, contracts.OrderStatusClosed,
	},
	contracts.OrderStatusProtectiveFailed: {
		contracts.OrderStatusFlattening, contracts.OrderStatusClosed,
	},
	contracts.OrderStatusFlattening: {
		contracts.OrderStatusClosed, contracts.OrderStatusFailed, contracts.OrderStatusUnknown,
	},
	contracts.OrderStatusUnknown: {
		contracts.OrderStatusAcknowledged, contracts.OrderStatusPartiallyFilled,
		contracts.OrderStatusFilled, contracts.OrderStatusCanceled,
		contracts.OrderStatusRejected, contracts.OrderStatusFailed, contracts.OrderStatusClosed,
	},
}

var terminal = map[contracts.OrderStatus]struct{}{
	contracts.OrderStatusClosed:   {},
	contracts.OrderStatusCanceled: {},
	contracts.OrderStatusRejected: {},
	contracts.OrderStatusFailed:   {},
}

// CanTransition reports whether from -> to is a legal state change.
func CanTransition(from, to contracts.OrderStatus) bool {
	for _, allowed := range transitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// IsTerminal reports whether no further transitions may leave the status.
func IsTerminal(status contracts.OrderStatus) bool {
	_, ok := terminal[status]
	return ok
}

// ValidateTransition returns a descriptive error for illegal transitions.
func ValidateTransition(from, to contracts.OrderStatus) error {
	if IsTerminal(from) {
		return fmt.Errorf("order status %q is terminal and cannot change to %q", from, to)
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("illegal order transition %q -> %q", from, to)
	}
	return nil
}
