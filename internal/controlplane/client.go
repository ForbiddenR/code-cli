package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"code-cli/internal/core"
)

// Client calls authenticated Claude control-plane endpoints.
type Client struct {
	baseURL        *url.URL
	httpClient     *http.Client
	userAgent      string
	defaultHeaders map[string]string
	authHeaders    map[string]string
	timeout        time.Duration
}

// NewClient creates a control-plane API client.
func NewClient(config Config) (*Client, error) {
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = core.DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("parse base url: missing scheme or host")
	}

	timeout := config.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{
		baseURL:        parsed,
		httpClient:     httpClient,
		userAgent:      config.UserAgent,
		defaultHeaders: cloneHeaders(config.DefaultHeaders),
		authHeaders:    cloneHeaders(config.AuthHeaders),
		timeout:        timeout,
	}, nil
}

func (c *Client) doJSON(ctx context.Context, method string, path string, query url.Values, body any, out any, opts ...CallOption) error {
	callOptions := ApplyOptions(opts...)
	timeout := c.timeout
	if callOptions.Timeout > 0 {
		timeout = callOptions.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		request.Header.Set("User-Agent", c.userAgent)
	}
	applyHeaders(request.Header, c.defaultHeaders)
	applyHeaders(request.Header, c.authHeaders)
	applyHeaders(request.Header, callOptions.Headers)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return classifyTransportError(err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	if out == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) endpoint(path string, query url.Values) string {
	endpoint := *c.baseURL
	basePath := strings.TrimRight(endpoint.Path, "/")
	path = "/" + strings.TrimLeft(path, "/")
	endpoint.Path = basePath + path
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func classifyTransportError(err error) *core.APIError {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &core.APIError{Kind: core.APIErrorAbort, Message: err.Error(), Retryable: false, Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &core.APIError{Kind: core.APIErrorTimeout, Message: err.Error(), Retryable: true, Cause: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		kind := core.APIErrorNetwork
		if netErr.Timeout() {
			kind = core.APIErrorTimeout
		}
		return &core.APIError{Kind: kind, Message: err.Error(), Retryable: true, Cause: err}
	}
	return &core.APIError{Kind: core.APIErrorNetwork, Message: err.Error(), Retryable: true, Cause: err}
}

func responseError(response *http.Response) *core.APIError {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	message := strings.TrimSpace(string(body))
	if extracted := extractErrorMessage(body); extracted != "" {
		message = extracted
	}
	if message == "" {
		message = response.Status
	}
	return &core.APIError{
		Kind:       errorKindForStatus(response.StatusCode),
		StatusCode: response.StatusCode,
		Message:    message,
		RequestID:  responseRequestID(response),
		Retryable:  retryableStatus(response.StatusCode),
	}
}

func extractErrorMessage(body []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	if envelope.Error.Message != "" {
		return envelope.Error.Message
	}
	return envelope.Message
}

func errorKindForStatus(status int) core.APIErrorKind {
	switch status {
	case http.StatusUnauthorized:
		return core.APIErrorAuth
	case http.StatusForbidden:
		return core.APIErrorPermission
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return core.APIErrorTimeout
	case http.StatusRequestEntityTooLarge:
		return core.APIErrorContextLength
	case http.StatusTooManyRequests:
		return core.APIErrorRateLimit
	case 529:
		return core.APIErrorOverloaded
	}
	if status >= 500 {
		return core.APIErrorServer
	}
	if status >= 400 {
		return core.APIErrorInvalidRequest
	}
	return core.APIErrorUnknown
}

func retryableStatus(status int) bool {
	switch errorKindForStatus(status) {
	case core.APIErrorRateLimit, core.APIErrorOverloaded, core.APIErrorTimeout, core.APIErrorServer:
		return true
	default:
		return false
	}
}

func responseRequestID(response *http.Response) string {
	if response == nil {
		return ""
	}
	if requestID := response.Header.Get("request-id"); requestID != "" {
		return requestID
	}
	return response.Header.Get("x-request-id")
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	clone := make(map[string]string, len(headers))
	maps.Copy(clone, headers)
	return clone
}

func applyHeaders(target http.Header, headers map[string]string) {
	for name, value := range headers {
		if value == "" {
			continue
		}
		target.Set(name, value)
	}
}
