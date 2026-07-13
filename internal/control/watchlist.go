package control

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const maxRuntimeWatchlistSize = 12

var (
	ErrInvalidWatchlist  = errors.New("watchlist must contain 1 to 12 valid symbols")
	runtimeSymbolPattern = regexp.MustCompile(`^[A-Z0-9]{2,20}/[A-Z0-9]{2,12}(?::[A-Z0-9]{2,12})?$`)
)

// normalizeRuntimeWatchlist is the policy boundary shared by every runtime
// settings caller. Venue-specific execution constraints stay out of handlers
// and UI code, so API mutations cannot bypass them.
func normalizeRuntimeWatchlist(source []string, requireOKXDemoPerpetual bool) ([]string, error) {
	if len(source) == 0 || len(source) > maxRuntimeWatchlistSize {
		return nil, ErrInvalidWatchlist
	}
	result := make([]string, 0, len(source))
	seen := make(map[string]struct{}, len(source))
	for _, raw := range source {
		symbol := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(raw), " ", ""))
		if !runtimeSymbolPattern.MatchString(symbol) {
			return nil, fmt.Errorf("%w: invalid symbol %q", ErrInvalidWatchlist, raw)
		}
		if requireOKXDemoPerpetual && !strings.HasSuffix(symbol, "/USDT:USDT") {
			return nil, fmt.Errorf(
				"%w: OKX Demo requires USDT perpetual symbols such as BTC/USDT:USDT",
				ErrInvalidWatchlist,
			)
		}
		if _, duplicate := seen[symbol]; duplicate {
			continue
		}
		seen[symbol] = struct{}{}
		result = append(result, symbol)
	}
	if len(result) == 0 {
		return nil, ErrInvalidWatchlist
	}
	return result, nil
}
