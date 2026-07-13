package runtime

import (
	"fmt"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

// ExecutionTarget is the value object passed from venue adapters to a mode
// policy. Runtime safety and risk isolation therefore depend on capabilities,
// not concrete Paper/OKX implementations.
type ExecutionTarget struct {
	VenueID     string
	Kind        venue.Kind
	Environment venue.Environment
	Writable    bool
}

// ModePolicy is the Strategy boundary for runtime modes. A mode owns both the
// execution permission and the durable risk namespace for an account context.
type ModePolicy interface {
	Name() string
	ValidateExecution(ExecutionTarget) error
	RiskStateScope(ExecutionTarget) string
}

type paperModePolicy struct{}
type liveModePolicy struct{}

// ResolveModePolicy keeps string parsing at the configuration boundary. The
// rest of the runtime works with behavior instead of mode conditionals.
func ResolveModePolicy(mode string) (ModePolicy, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "paper":
		return paperModePolicy{}, nil
	case "live":
		return liveModePolicy{}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime mode %q", mode)
	}
}

func (paperModePolicy) Name() string { return "paper" }

func (paperModePolicy) ValidateExecution(target ExecutionTarget) error {
	localPaper := target.Environment == venue.EnvironmentPaper &&
		target.Kind == venue.KindPaper && target.VenueID == "paper" && target.Writable
	okxDemo := target.Environment == venue.EnvironmentDemo &&
		target.Kind == venue.KindCEX && target.VenueID == "okx" && target.Writable
	if localPaper || okxDemo {
		return nil
	}
	return unsupportedExecution(target)
}

func (paperModePolicy) RiskStateScope(target ExecutionTarget) string {
	switch target.Environment {
	case venue.EnvironmentPaper:
		return "paper"
	case venue.EnvironmentDemo:
		return "demo:" + target.VenueID
	default:
		return "paper:" + string(target.Environment) + ":" + target.VenueID
	}
}

func (liveModePolicy) Name() string { return "live" }

func (liveModePolicy) ValidateExecution(target ExecutionTarget) error {
	return unsupportedExecution(target)
}

func (liveModePolicy) RiskStateScope(target ExecutionTarget) string {
	return "live:" + target.VenueID
}

func unsupportedExecution(target ExecutionTarget) error {
	return fmt.Errorf(
		"%w: venue=%q kind=%q environment=%q writable=%t",
		ErrLiveExecutionDisabled,
		target.VenueID,
		target.Kind,
		target.Environment,
		target.Writable,
	)
}

func executionTarget(identity venue.ExecutionIdentity) ExecutionTarget {
	return ExecutionTarget{
		VenueID: identity.VenueID, Kind: identity.Kind,
		Environment: identity.Environment, Writable: identity.Writable,
	}
}

func (state RuntimeState) executionTarget() ExecutionTarget {
	target := ExecutionTarget{
		VenueID: state.ExecutionVenue,
		Kind:    venue.KindCEX, Environment: venue.EnvironmentLive,
	}
	if state.ExecutionVenue == "paper" {
		target.Kind = venue.KindPaper
		target.Environment = venue.EnvironmentPaper
		target.Writable = true
	} else if state.ExecutionDemo {
		target.Environment = venue.EnvironmentDemo
		target.Writable = true
	}
	return target
}
