package venue

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type CEXErrorKind string

const (
	CEXErrorValidation  CEXErrorKind = "validation"
	CEXErrorAuth        CEXErrorKind = "auth"
	CEXErrorRateLimit   CEXErrorKind = "rate_limit"
	CEXErrorTemporary   CEXErrorKind = "temporary"
	CEXErrorUpstream    CEXErrorKind = "upstream"
	CEXErrorDecode      CEXErrorKind = "decode"
	CEXErrorUnsupported CEXErrorKind = "unsupported"
	CEXErrorDisabled    CEXErrorKind = "disabled"
)

var (
	ErrCEXTradingDisabled = errors.New("Go 首版 CEX 真实下单硬禁用")
	ErrCEXUnsupported     = errors.New("exchange operation is unsupported")
)

// CEXError preserves a stable classification while retaining exchange status
// and codes for observability. Error strings never include credentials or
// signed query values.
type CEXError struct {
	Kind       CEXErrorKind
	Exchange   string
	Operation  string
	StatusCode int
	Code       string
	Message    string
	RetryAfter time.Duration
	Err        error
}

func (err *CEXError) Error() string {
	if err == nil {
		return "<nil>"
	}
	detail := err.Message
	if detail == "" && err.Err != nil {
		detail = err.Err.Error()
	}
	if detail == "" {
		detail = "exchange request failed"
	}
	if err.Code != "" {
		return fmt.Sprintf("%s %s %s (%s): %s", err.Exchange, err.Operation, err.Kind, err.Code, detail)
	}
	return fmt.Sprintf("%s %s %s: %s", err.Exchange, err.Operation, err.Kind, detail)
}

func (err *CEXError) Unwrap() error { return err.Err }

func (err *CEXError) Retryable() bool {
	return err != nil && (err.Kind == CEXErrorRateLimit || err.Kind == CEXErrorTemporary)
}

func classifyHTTPError(exchange, operation string, response *http.Response, message string) *CEXError {
	kind := CEXErrorUpstream
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = CEXErrorAuth
	case http.StatusTooManyRequests:
		kind = CEXErrorRateLimit
	default:
		if response.StatusCode >= 500 {
			kind = CEXErrorTemporary
		}
	}
	return &CEXError{
		Kind:       kind,
		Exchange:   exchange,
		Operation:  operation,
		StatusCode: response.StatusCode,
		Message:    message,
		RetryAfter: parseRetryAfter(response.Header.Get("Retry-After")),
	}
}

func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if stamp, err := http.ParseTime(value); err == nil {
		if delay := time.Until(stamp); delay > 0 {
			return delay
		}
	}
	return 0
}

func cexErrorKind(err error) CEXErrorKind {
	var classified *CEXError
	if errors.As(err, &classified) {
		return classified.Kind
	}
	return ""
}
