package llm

import (
	"errors"
	"fmt"
)

var (
	ErrDisabled            = errors.New("llm is disabled")
	ErrCircuitOpen         = errors.New("llm circuit breaker is open")
	ErrBudgetExceeded      = errors.New("llm budget exceeded")
	ErrDailyBudgetExceeded = errors.New("daily llm budget exceeded")
	ErrInvalidResponse     = errors.New("llm returned an invalid response")
)

// ProviderError deliberately omits response bodies, request payloads, URLs,
// and credentials. It is safe to expose through structured logs.
type ProviderError struct {
	Provider   string
	Operation  string
	StatusCode int
	Transient  bool
	Cause      error
}

func (e *ProviderError) Error() string {
	kind := "permanent"
	if e.Transient {
		kind = "transient"
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("%s %s %s error (HTTP %d)", e.Provider, e.Operation, kind, e.StatusCode)
	}
	return fmt.Sprintf("%s %s %s error", e.Provider, e.Operation, kind)
}

func (e *ProviderError) Unwrap() error { return e.Cause }

func isTransient(err error) bool {
	var providerError *ProviderError
	if errors.As(err, &providerError) {
		return providerError.Transient
	}
	return false
}
