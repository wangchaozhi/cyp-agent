// Package agents contains read-only analysis and proposal generation. It does
// not import venue or approval packages and exposes no execution capability.
package agents

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

var (
	credentialAssignment = regexp.MustCompile(`(?i)(api[_-]?(?:key|secret)|private[_-]?key|authorization|password|token)\s*[:=]\s*[^\s;,]+`)
	bearerCredential     = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]{8,}`)
	skCredential         = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`)
	pemPrivateKey        = regexp.MustCompile(`(?is)-----BEGIN [^-\r\n]*PRIVATE KEY-----.*?-----END [^-\r\n]*PRIVATE KEY-----`)
)

type LLM interface {
	Enabled() bool
	Text(context.Context, string, string, bool) (string, error)
	JSON(context.Context, string, string, json.RawMessage, any, bool) error
}

type AgentContext struct {
	LLM       LLM
	AllowPerp bool
	Lessons   []string
}

// Context is a convenience alias used by orchestrators.
type Context = AgentContext

func (ctx AgentContext) LLMEnabled() bool { return ctx.LLM != nil && ctx.LLM.Enabled() }

type Vote struct {
	Sign   float64
	Weight float64
	Signal contracts.Signal
}

func Blend(votes []Vote) (contracts.Stance, float64) {
	total := 0.0
	net := 0.0
	for _, vote := range votes {
		if math.IsNaN(vote.Sign) || math.IsInf(vote.Sign, 0) ||
			math.IsNaN(vote.Weight) || math.IsInf(vote.Weight, 0) || vote.Weight <= 0 {
			continue
		}
		sign := math.Max(-1, math.Min(1, vote.Sign))
		total += vote.Weight
		net += sign * vote.Weight
	}
	if total <= 0 {
		return contracts.StanceNeutral, 0.2
	}
	net /= total
	stance := contracts.StanceNeutral
	if net > 0.15 {
		stance = contracts.StanceBullish
	} else if net < -0.15 {
		stance = contracts.StanceBearish
	}
	return stance, math.Min(1, math.Abs(net))
}

func StanceSign(stance contracts.Stance) float64 {
	switch stance {
	case contracts.StanceBullish:
		return 1
	case contracts.StanceBearish:
		return -1
	default:
		return 0
	}
}

func redactSensitive(value string) string {
	value = pemPrivateKey.ReplaceAllString(value, "[REDACTED PRIVATE KEY]")
	value = bearerCredential.ReplaceAllString(value, "Bearer [REDACTED]")
	value = credentialAssignment.ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.IndexAny(match, ":="); index >= 0 {
			return match[:index+1] + "[REDACTED]"
		}
		return "[REDACTED]"
	})
	return skCredential.ReplaceAllString(value, "[REDACTED]")
}
