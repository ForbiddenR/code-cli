package anthropicapi

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"code-cli/internal/core"
)

type retrySleeper func(context.Context, time.Duration) error

func retryAPI[T any](ctx context.Context, config core.RetryConfig, sleep retrySleeper, operation func(context.Context, int) (T, error)) (T, error) {
	config = config.WithDefaults()
	if sleep == nil {
		sleep = sleepContext
	}

	var zero T
	for attempt := 1; attempt <= config.MaxRetries+1; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, ClassifyError(err)
		}

		result, err := operation(ctx, attempt)
		if err == nil {
			return result, nil
		}

		apiErr := ClassifyError(err)
		if apiErr == nil {
			return zero, nil
		}
		if !apiErr.Retryable || attempt > config.MaxRetries {
			return zero, apiErr
		}

		delay := retryDelay(config, attempt)
		if retryAfter, ok := retryAfterDelay(err); ok {
			delay = retryAfter
		}
		if err := sleep(ctx, delay); err != nil {
			return zero, ClassifyError(err)
		}
	}

	return zero, ClassifyError(ctx.Err())
}

func retryDelay(config core.RetryConfig, retryAttempt int) time.Duration {
	config = config.WithDefaults()
	if retryAttempt < 1 {
		retryAttempt = 1
	}

	delay := config.BaseDelay
	for i := 1; i < retryAttempt; i++ {
		if delay >= config.MaxDelay/2 {
			delay = config.MaxDelay
			break
		}
		delay *= 2
	}
	if delay > config.MaxDelay {
		delay = config.MaxDelay
	}
	if config.JitterFraction > 0 && delay > 0 {
		jitter := time.Duration(rand.Float64() * config.JitterFraction * float64(delay))
		delay += jitter
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryAfterDelay(err error) (time.Duration, bool) {
	for current := err; current != nil; current = errors.Unwrap(current) {
		if headers := headersFromValue(current); headers != nil {
			if delay, ok := parseRetryAfter(headers.Get("retry-after")); ok {
				return delay, true
			}
		}
	}
	return 0, false
}

func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := time.Until(when)
		if delay <= 0 {
			return 0, false
		}
		return delay, true
	}
	return 0, false
}

type responseGetter interface {
	Response() *http.Response
}

type headersGetter interface {
	Headers() http.Header
}

type headerGetter interface {
	Header() http.Header
}

func headersFromValue(value any) http.Header {
	if value == nil {
		return nil
	}
	if getter, ok := value.(headersGetter); ok {
		return getter.Headers()
	}
	if getter, ok := value.(headerGetter); ok {
		return getter.Header()
	}
	if getter, ok := value.(responseGetter); ok {
		if response := getter.Response(); response != nil {
			return response.Header
		}
	}

	v := reflect.ValueOf(value)
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return nil
	}

	for _, name := range []string{"Headers", "Header"} {
		field := v.FieldByName(name)
		if field.IsValid() && field.CanInterface() {
			if headers, ok := field.Interface().(http.Header); ok {
				return headers
			}
		}
	}
	field := v.FieldByName("Response")
	if field.IsValid() && field.CanInterface() {
		if response, ok := field.Interface().(*http.Response); ok && response != nil {
			return response.Header
		}
	}
	return nil
}
