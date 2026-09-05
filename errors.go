package internetdata

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrorKind says why a request failed.
//
// KindRateLimited and KindQuotaExceeded both arrive as HTTP 429 and are NOT the
// same thing. A rate limit is the API protecting itself, carries Retry-After,
// and retrying works. A spent quota carries no such header and retrying will
// not help until the window rolls over or the limit is raised. The header is
// the only thing that distinguishes them.
type ErrorKind string

const (
	KindBadRequest    ErrorKind = "bad_request"
	KindUnauthorized  ErrorKind = "unauthorized"
	KindForbidden     ErrorKind = "forbidden"
	KindRateLimited   ErrorKind = "rate_limited"
	KindQuotaExceeded ErrorKind = "quota_exceeded"
	KindServerError   ErrorKind = "server_error"
	KindNetwork       ErrorKind = "network"
)

// Error is what every failure from this package unwraps to. Recover it with
// errors.As and branch on Kind.
type Error struct {
	Kind ErrorKind
	// Message is the API's own result code, or a transport failure's text. The
	// codes are deliberately not an enum on the wire, so one added later stays
	// readable to a client built today.
	Message string
	// StatusCode is the HTTP status, or 0 when no response was received.
	StatusCode int
	// RetryAfter is how long the server asked us to wait. Zero when it did not
	// ask, which on a 429 means an allowance is spent rather than throttled.
	RetryAfter time.Duration

	cause error
}

func (e *Error) Error() string {
	if e.StatusCode == 0 {
		return fmt.Sprintf("internetdata: %s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("internetdata: %s (HTTP %d): %s", e.Kind, e.StatusCode, e.Message)
}

// Retryable reports whether retrying this exact request could succeed.
func (e *Error) Retryable() bool {
	switch e.Kind {
	case KindRateLimited, KindServerError, KindNetwork:
		return true
	}
	return false
}

func (e *Error) Unwrap() error {
	return e.cause
}

func errorFromResponse(status int, header http.Header, body []byte) *Error {
	message := messageOf(body)
	if message == "" {
		message = fmt.Sprintf("request failed with status %d", status)
	}
	retryAfter := parseRetryAfter(header.Get("Retry-After"))

	switch status {
	case http.StatusTooManyRequests:
		// Present means transient, absent means an allowance is spent. Nothing
		// else in the response separates the two.
		if retryAfter <= 0 {
			return &Error{Kind: KindQuotaExceeded, Message: message, StatusCode: status}
		}
		return &Error{
			Kind: KindRateLimited, Message: message, StatusCode: status, RetryAfter: retryAfter,
		}
	case http.StatusBadRequest:
		return &Error{Kind: KindBadRequest, Message: message, StatusCode: status}
	case http.StatusUnauthorized:
		return &Error{Kind: KindUnauthorized, Message: message, StatusCode: status}
	case http.StatusForbidden:
		return &Error{Kind: KindForbidden, Message: message, StatusCode: status}
	}
	// Any other 4xx is a CLIENT error, and 404 is the one this API actually
	// serves: an id it does not publish. Falling through to the server_error
	// default would make it retryable, so a misspelled id would be requested
	// three times before failing. Only 5xx and transport failures are worth a
	// retry.
	if status < http.StatusInternalServerError {
		return &Error{Kind: KindBadRequest, Message: message, StatusCode: status}
	}
	return &Error{Kind: KindServerError, Message: message, StatusCode: status}
}

// A failure with no response of its own: a refused connection, a timeout, a
// canceled context, or a body the client could not decode. All are worth
// another attempt, and the cause is kept so errors.Is still sees it.
func errorFromTransport(err error) *Error {
	return &Error{Kind: KindNetwork, Message: err.Error(), cause: err}
}

// Every refusal carries `{"rc": "<CODE>"}`, which is more use to a caller than
// the status alone: NOT_LICENSED and LICENSE_EXPIRED are both 403 and mean
// different things to do next.
func messageOf(body []byte) string {
	var envelope struct {
		Rc string `json:"rc"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	return envelope.Rc
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	// The header also permits an HTTP date.
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	return max(0, time.Until(when))
}
