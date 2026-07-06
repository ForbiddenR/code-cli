package core

import "fmt"

// APIErrorKind classifies API failures for retry and user-facing handling.
type APIErrorKind string

const (
	APIErrorAuth           APIErrorKind = "auth"
	APIErrorPermission     APIErrorKind = "permission"
	APIErrorRateLimit      APIErrorKind = "rate_limit"
	APIErrorOverloaded     APIErrorKind = "overloaded"
	APIErrorNetwork        APIErrorKind = "network"
	APIErrorTimeout        APIErrorKind = "timeout"
	APIErrorAbort          APIErrorKind = "abort"
	APIErrorInvalidRequest APIErrorKind = "invalid_request"
	APIErrorContextLength  APIErrorKind = "context_length"
	APIErrorServer         APIErrorKind = "server"
	APIErrorUnknown        APIErrorKind = "unknown"
)

// APIError is the normalized error shape produced by the API boundary.
type APIError struct {
	Kind       APIErrorKind
	StatusCode int
	Message    string
	RequestID  string
	Retryable  bool
	Cause      error
}

func (e *APIError) Error() string {
	if e == nil {
		return "api error"
	}
	if e.StatusCode == 0 {
		return fmt.Sprintf("%s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("%s: status %d: %s", e.Kind, e.StatusCode, e.Message)
}

func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
