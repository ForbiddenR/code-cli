package sessioningress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"code-cli/internal/core"
)

// Client calls session ingress transcript endpoints.
type Client struct {
	baseURL           *url.URL
	httpClient        *http.Client
	authToken         string
	orgUUID           string
	timeout           time.Duration
	maxRetries        int
	baseDelay         time.Duration
	sleep             func(time.Duration)
	afterLastCompact  bool
	lastUUIDBySession map[string]string
}

// NewClient creates a session ingress client.
func NewClient(config Config) (*Client, error) {
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
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
	maxRetries := config.MaxRetries
	if maxRetries <= 0 {
		maxRetries = DefaultMaxRetries
	}
	baseDelay := config.BaseDelay
	if baseDelay <= 0 {
		baseDelay = DefaultBaseDelay
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	sleep := config.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	return &Client{
		baseURL:           parsed,
		httpClient:        httpClient,
		authToken:         config.AuthToken,
		orgUUID:           config.OrgUUID,
		timeout:           timeout,
		maxRetries:        maxRetries,
		baseDelay:         baseDelay,
		sleep:             sleep,
		afterLastCompact:  config.AfterLastCompact,
		lastUUIDBySession: map[string]string{},
	}, nil
}

// AppendSessionLog appends one transcript entry using optimistic Last-Uuid ordering.
func (c *Client) AppendSessionLog(ctx context.Context, sessionID string, entry TranscriptEntry) (bool, error) {
	if strings.TrimSpace(sessionID) == "" {
		return false, fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(entry.UUID) == "" {
		return false, fmt.Errorf("entry uuid is required")
	}
	if len(entry.Body) == 0 {
		return false, fmt.Errorf("entry body is required")
	}

	path := "/v1/session_ingress/session/" + url.PathEscape(sessionID)
	var lastErr error
	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		headers := c.authHeaders()
		if lastUUID := c.lastUUIDBySession[sessionID]; lastUUID != "" {
			headers.Set("Last-Uuid", lastUUID)
		}
		response, err := c.do(ctx, http.MethodPut, path, nil, bytes.NewReader(entry.Body), headers)
		if err != nil {
			lastErr = err
		} else {
			ok, retry, err := c.handleAppendResponse(ctx, sessionID, entry.UUID, response)
			if ok || !retry {
				return ok, err
			}
			lastErr = err
		}
		if attempt == c.maxRetries {
			break
		}
		if err := c.wait(ctx, attempt); err != nil {
			return false, err
		}
	}
	if lastErr != nil {
		return false, lastErr
	}
	return false, nil
}

// FetchSessionLogs fetches transcript entries for hydration.
func (c *Client) FetchSessionLogs(ctx context.Context, sessionID string) ([]Entry, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session id is required")
	}
	query := url.Values{}
	if c.afterLastCompact {
		query.Set("after_last_compact", "true")
	}
	response, err := c.do(ctx, http.MethodGet, "/v1/session_ingress/session/"+url.PathEscape(sessionID), query, nil, c.authHeaders())
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusOK {
		entries, err := decodeLoglines(response.Body)
		if err != nil {
			return nil, err
		}
		if lastUUID := findLastUUID(entries); lastUUID != "" {
			c.lastUUIDBySession[sessionID] = lastUUID
		}
		return entries, nil
	}
	if response.StatusCode == http.StatusNotFound {
		return []Entry{}, nil
	}
	return nil, responseError(response)
}

// ClearSession clears cached optimistic ordering state for one session.
func (c *Client) ClearSession(sessionID string) {
	delete(c.lastUUIDBySession, sessionID)
}

// ClearAllSessions clears all cached optimistic ordering state.
func (c *Client) ClearAllSessions() {
	clear(c.lastUUIDBySession)
}

func (c *Client) handleAppendResponse(ctx context.Context, sessionID string, entryUUID string, response *http.Response) (bool, bool, error) {
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated {
		c.lastUUIDBySession[sessionID] = entryUUID
		return true, false, nil
	}
	if response.StatusCode == http.StatusUnauthorized {
		return false, false, responseError(response)
	}
	if response.StatusCode == http.StatusConflict {
		serverLastUUID := response.Header.Get("x-last-uuid")
		if serverLastUUID == entryUUID {
			c.lastUUIDBySession[sessionID] = entryUUID
			return true, false, nil
		}
		if serverLastUUID == "" {
			entries, err := c.FetchSessionLogs(ctx, sessionID)
			if err != nil {
				return false, false, err
			}
			serverLastUUID = findLastUUID(entries)
		}
		if serverLastUUID == "" {
			return false, false, responseError(response)
		}
		c.lastUUIDBySession[sessionID] = serverLastUUID
		return false, true, fmt.Errorf("session append conflict")
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		return false, true, responseError(response)
	}
	return false, false, responseError(response)
}

func (c *Client) do(ctx context.Context, method string, path string, query url.Values, body io.Reader, headers http.Header) (*http.Response, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	return response, nil
}

func (c *Client) authHeaders() http.Header {
	headers := http.Header{}
	if c.authToken != "" {
		headers.Set("Authorization", "Bearer "+c.authToken)
	}
	if c.orgUUID != "" {
		headers.Set("x-organization-uuid", c.orgUUID)
	}
	return headers
}

func (c *Client) endpoint(path string, query url.Values) string {
	endpoint := *c.baseURL
	basePath := strings.TrimRight(endpoint.Path, "/")
	path = "/" + strings.TrimLeft(path, "/")
	endpoint.Path = basePath + path
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func (c *Client) wait(ctx context.Context, attempt int) error {
	delay := min(c.baseDelay*time.Duration(1<<(attempt-1)), 8*time.Second)
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c.sleep(delay)
	return nil
}

func decodeLoglines(reader io.Reader) ([]Entry, error) {
	var response struct {
		Loglines []json.RawMessage `json:"loglines"`
	}
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode session logs: %w", err)
	}
	if response.Loglines == nil {
		return nil, fmt.Errorf("decode session logs: missing loglines")
	}
	entries := make([]Entry, 0, len(response.Loglines))
	for _, logline := range response.Loglines {
		entries = append(entries, Entry(logline))
	}
	return entries, nil
}

func findLastUUID(entries []Entry) string {
	for i := len(entries) - 1; i >= 0; i-- {
		var entry struct {
			UUID string `json:"uuid"`
		}
		if json.Unmarshal(entries[i], &entry) == nil && entry.UUID != "" {
			return entry.UUID
		}
	}
	return ""
}

func responseError(response *http.Response) *core.APIError {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	return &core.APIError{
		Kind:       errorKindForStatus(response.StatusCode),
		StatusCode: response.StatusCode,
		Message:    message,
		Retryable:  retryableStatus(response.StatusCode),
	}
}

func errorKindForStatus(status int) core.APIErrorKind {
	switch status {
	case http.StatusUnauthorized:
		return core.APIErrorAuth
	case http.StatusForbidden:
		return core.APIErrorPermission
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return core.APIErrorTimeout
	case http.StatusTooManyRequests:
		return core.APIErrorRateLimit
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
	case core.APIErrorRateLimit, core.APIErrorTimeout, core.APIErrorServer:
		return true
	default:
		return false
	}
}

func classifyTransportError(err error) *core.APIError {
	if err == nil {
		return nil
	}
	if netErr, ok := err.(net.Error); ok {
		kind := core.APIErrorNetwork
		if netErr.Timeout() {
			kind = core.APIErrorTimeout
		}
		return &core.APIError{Kind: kind, Message: err.Error(), Retryable: true, Cause: err}
	}
	return &core.APIError{Kind: core.APIErrorNetwork, Message: err.Error(), Retryable: true, Cause: err}
}
