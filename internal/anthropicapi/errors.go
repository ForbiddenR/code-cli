package anthropicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"

	"code-cli/internal/core"

	"github.com/anthropics/anthropic-sdk-go/shared"
)

type sdkAPIError interface {
	error
	Type() shared.ErrorType
	RawJSON() string
}

// ClassifyError converts SDK, network, timeout, and cancellation errors into the
// normalized API error shape used by the API boundary.
func ClassifyError(err error) *core.APIError {
	if err == nil {
		return nil
	}

	if errors.Is(err, context.Canceled) {
		return &core.APIError{
			Kind:      core.APIErrorAbort,
			Message:   err.Error(),
			Retryable: false,
			Cause:     err,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &core.APIError{
			Kind:      core.APIErrorTimeout,
			Message:   err.Error(),
			Retryable: true,
			Cause:     err,
		}
	}

	var apiErr sdkAPIError
	if errors.As(err, &apiErr) {
		statusCode := intField(apiErr, "StatusCode")
		requestID := stringField(apiErr, "RequestID")
		message, responseRequestID := apiErrorMessage(apiErr)
		if requestID == "" {
			requestID = responseRequestID
		}
		kind := apiErrorKind(apiErr.Type(), statusCode, message)
		return &core.APIError{
			Kind:       kind,
			StatusCode: statusCode,
			Message:    message,
			RequestID:  requestID,
			Retryable:  retryableAPIError(kind),
			Cause:      err,
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		kind := core.APIErrorNetwork
		if netErr.Timeout() {
			kind = core.APIErrorTimeout
		}
		return &core.APIError{
			Kind:      kind,
			Message:   err.Error(),
			Retryable: true,
			Cause:     err,
		}
	}

	return &core.APIError{
		Kind:      core.APIErrorUnknown,
		Message:   err.Error(),
		Retryable: false,
		Cause:     err,
	}
}

func apiErrorMessage(err sdkAPIError) (message string, requestID string) {
	message = err.Error()
	raw := err.RawJSON()
	if raw == "" {
		return message, ""
	}

	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil {
		return message, ""
	}
	if envelope.Error.Message != "" {
		message = envelope.Error.Message
	}
	return message, envelope.RequestID
}

func apiErrorKind(errorType shared.ErrorType, statusCode int, message string) core.APIErrorKind {
	switch errorType {
	case shared.ErrorTypeAuthenticationError:
		return core.APIErrorAuth
	case shared.ErrorTypePermissionError, shared.ErrorTypeBillingError:
		return core.APIErrorPermission
	case shared.ErrorTypeRateLimitError:
		return core.APIErrorRateLimit
	case shared.ErrorTypeOverloadedError:
		return core.APIErrorOverloaded
	case shared.ErrorTypeTimeoutError:
		return core.APIErrorTimeout
	case shared.ErrorTypeInvalidRequestError:
		if looksLikeContextLength(message) {
			return core.APIErrorContextLength
		}
		return core.APIErrorInvalidRequest
	case shared.ErrorTypeAPIError:
		return core.APIErrorServer
	}

	switch statusCode {
	case 400:
		if looksLikeContextLength(message) {
			return core.APIErrorContextLength
		}
		return core.APIErrorInvalidRequest
	case 401:
		return core.APIErrorAuth
	case 403:
		return core.APIErrorPermission
	case 408, 504:
		return core.APIErrorTimeout
	case 413:
		return core.APIErrorContextLength
	case 429:
		return core.APIErrorRateLimit
	case 529:
		return core.APIErrorOverloaded
	}
	if statusCode >= 500 {
		return core.APIErrorServer
	}
	return core.APIErrorUnknown
}

func retryableAPIError(kind core.APIErrorKind) bool {
	switch kind {
	case core.APIErrorRateLimit, core.APIErrorOverloaded, core.APIErrorNetwork, core.APIErrorTimeout, core.APIErrorServer:
		return true
	default:
		return false
	}
}

func looksLikeContextLength(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "context") && (strings.Contains(message, "length") || strings.Contains(message, "window") || strings.Contains(message, "too long"))
}

func intField(value any, name string) int {
	field := reflectedField(value, name)
	if !field.IsValid() {
		return 0
	}
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(field.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(field.Uint())
	default:
		return 0
	}
}

func stringField(value any, name string) string {
	field := reflectedField(value, name)
	if !field.IsValid() || field.Kind() != reflect.String {
		return ""
	}
	return field.String()
}

func reflectedField(value any, name string) reflect.Value {
	v := reflect.ValueOf(value)
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return v.FieldByName(name)
}
