package venue

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Keep the skew comfortably below the five-second order expiry window. A
// host that cannot maintain ordinary NTP accuracy is not fit to open live
// positions even though OKX signatures allow a wider tolerance.
const maxOKXLiveClockSkew = 2 * time.Second

// ValidateLiveReadiness verifies the properties that static environment
// variables cannot prove. It is intentionally mandatory only for live OKX:
// Demo remains useful for exercising unsupported account configurations.
func (venue *CEXVenue) ValidateLiveReadiness(ctx context.Context) error {
	if ctx == nil {
		return &CEXError{Kind: CEXErrorValidation, Exchange: venue.id, Operation: "live_readiness", Message: "context is required"}
	}
	if !venue.LiveTradingEnabled() {
		return &CEXError{Kind: CEXErrorDisabled, Exchange: venue.id, Operation: "live_readiness", Message: "OKX live trading is not enabled"}
	}

	var clockPayload struct {
		Code string `json:"code"`
		Data []struct {
			Timestamp any `json:"ts"`
		} `json:"data"`
	}
	if err := venue.doJSON(ctx, http.MethodGet, "/api/v5/public/time", nil, false, &clockPayload); err != nil {
		return fmt.Errorf("verify OKX server time: %w", err)
	}
	if len(clockPayload.Data) == 0 {
		return &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "live_readiness", Message: "OKX server time response is empty"}
	}
	serverTime, err := milliseconds(clockPayload.Data[0].Timestamp)
	if err != nil {
		return &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "live_readiness", Message: "invalid OKX server time", Err: err}
	}
	skew := venue.now().UTC().Sub(serverTime).Abs()
	if skew > maxOKXLiveClockSkew {
		return &CEXError{
			Kind: CEXErrorValidation, Exchange: venue.id, Operation: "live_readiness",
			Message: fmt.Sprintf("local clock differs from OKX by %s; synchronize NTP before live trading", skew.Round(time.Millisecond)),
		}
	}

	var accountPayload struct {
		Code string `json:"code"`
		Data []struct {
			AccountLevel string `json:"acctLv"`
			PositionMode string `json:"posMode"`
			Permissions  string `json:"perm"`
			BoundIPs     string `json:"ip"`
		} `json:"data"`
	}
	if err := privateGET(venue, ctx, "/api/v5/account/config", &accountPayload); err != nil {
		return fmt.Errorf("verify OKX live account configuration: %w", err)
	}
	if len(accountPayload.Data) == 0 {
		return &CEXError{Kind: CEXErrorDecode, Exchange: venue.id, Operation: "live_readiness", Message: "OKX account configuration is empty"}
	}
	account := accountPayload.Data[0]
	if account.PositionMode != "net_mode" {
		return &CEXError{
			Kind: CEXErrorValidation, Exchange: venue.id, Operation: "live_readiness",
			Message: "OKX live account must use net_mode so full-position protective orders are verifiable",
		}
	}
	if account.AccountLevel != "2" && account.AccountLevel != "3" {
		return &CEXError{
			Kind: CEXErrorValidation, Exchange: venue.id, Operation: "live_readiness",
			Message: "OKX live account must use Futures mode (2) or Multi-currency margin mode (3); Portfolio margin is unsupported",
		}
	}
	permissions := commaSet(account.Permissions)
	if _, ok := permissions["trade"]; !ok {
		return &CEXError{Kind: CEXErrorAuth, Exchange: venue.id, Operation: "live_readiness", Message: "OKX API key is missing Trade permission"}
	}
	if _, ok := permissions["withdraw"]; ok {
		return &CEXError{Kind: CEXErrorAuth, Exchange: venue.id, Operation: "live_readiness", Message: "OKX API key must not have Withdraw permission"}
	}
	if strings.TrimSpace(account.BoundIPs) == "" {
		return &CEXError{Kind: CEXErrorAuth, Exchange: venue.id, Operation: "live_readiness", Message: "OKX live API key must be bound to an IP allowlist"}
	}
	return nil
}

func commaSet(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, item := range strings.Split(value, ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" {
			result[item] = struct{}{}
		}
	}
	return result
}
