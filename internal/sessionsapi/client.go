package sessionsapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"code-cli/internal/core"
)

// Client calls the CCR BYOC Sessions API.
type Client struct {
	baseURL          *url.URL
	httpClient       *http.Client
	accessToken      string
	orgUUID          string
	timeout          time.Duration
	sendEventTimeout time.Duration
	retryDelays      []time.Duration
	sleep            func(time.Duration)
}

// NewClient creates a Sessions API client.
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

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	sendEventTimeout := config.SendEventTimeout
	if sendEventTimeout <= 0 {
		sendEventTimeout = DefaultSendEventTimeout
	}
	retryDelays := config.RetryDelays
	if len(retryDelays) == 0 {
		retryDelays = DefaultRetryDelays
	}
	sleep := config.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	return &Client{
		baseURL:          parsed,
		httpClient:       httpClient,
		accessToken:      config.AccessToken,
		orgUUID:          config.OrgUUID,
		timeout:          timeout,
		sendEventTimeout: sendEventTimeout,
		retryDelays:      append([]time.Duration(nil), retryDelays...),
		sleep:            sleep,
	}, nil
}

// ListCodeSessions fetches sessions and transforms them into the legacy code-session shape.
func (c *Client) ListCodeSessions(ctx context.Context) ([]CodeSession, error) {
	var response ListSessionsResponse
	err := c.getWithRetry(ctx, "/v1/sessions", nil, &response)
	if err != nil {
		return nil, err
	}
	sessions := make([]CodeSession, 0, len(response.Data))
	for _, session := range response.Data {
		sessions = append(sessions, transformCodeSession(session))
	}
	return sessions, nil
}

// FetchSession fetches one raw session resource by ID.
func (c *Client) FetchSession(ctx context.Context, sessionID string) (SessionResource, error) {
	if strings.TrimSpace(sessionID) == "" {
		return SessionResource{}, fmt.Errorf("session id is required")
	}

	var session SessionResource
	response, err := c.do(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return SessionResource{}, err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
			return SessionResource{}, fmt.Errorf("decode session response: %w", err)
		}
		return session, nil
	}
	if response.StatusCode == http.StatusNotFound {
		return SessionResource{}, &core.APIError{Kind: core.APIErrorInvalidRequest, StatusCode: response.StatusCode, Message: "session not found: " + sessionID, Retryable: false}
	}
	if response.StatusCode == http.StatusUnauthorized {
		return SessionResource{}, &core.APIError{Kind: core.APIErrorAuth, StatusCode: response.StatusCode, Message: "session expired. please run /login to sign in again", Retryable: false}
	}
	return SessionResource{}, responseError(response)
}

// SendEventToRemoteSession sends one user message event to an existing remote session.
func (c *Client) SendEventToRemoteSession(ctx context.Context, sessionID string, messageContent RemoteMessageContent, opts SendEventOptions) (bool, error) {
	if strings.TrimSpace(sessionID) == "" {
		return false, fmt.Errorf("session id is required")
	}
	if messageContent == nil {
		return false, fmt.Errorf("message content is required")
	}

	eventUUID := opts.UUID
	if strings.TrimSpace(eventUUID) == "" {
		var err error
		eventUUID, err = randomUUID()
		if err != nil {
			return false, err
		}
	}
	requestBody := sendEventsRequest{Events: []remoteSessionEvent{{
		UUID:            eventUUID,
		SessionID:       sessionID,
		Type:            "user",
		ParentToolUseID: nil,
		Message: remoteMessage{
			Role:    "user",
			Content: messageContent,
		},
	}}}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return false, fmt.Errorf("marshal session event: %w", err)
	}
	response, err := c.doWithBodyTimeout(ctx, c.sendEventTimeout, http.MethodPost, "/v1/sessions/"+url.PathEscape(sessionID)+"/events", nil, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated {
		_, _ = io.Copy(io.Discard, response.Body)
		return true, nil
	}
	return false, responseError(response)
}

// OAuthHeaders returns the shared OAuth headers used by teleport API requests.
func OAuthHeaders(accessToken string) http.Header {
	headers := http.Header{}
	if accessToken != "" {
		headers.Set("Authorization", "Bearer "+accessToken)
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("anthropic-version", AnthropicVersion)
	return headers
}

func (c *Client) getWithRetry(ctx context.Context, path string, query url.Values, out any) error {
	var lastErr error
	for attempt := 0; attempt <= len(c.retryDelays); attempt++ {
		response, err := c.do(ctx, http.MethodGet, path, query)
		if err != nil {
			lastErr = err
		} else {
			retry, err := decodeGETResponse(response, out)
			if err == nil || !retry || attempt == len(c.retryDelays) {
				return err
			}
			lastErr = err
		}
		if attempt == len(c.retryDelays) || !isTransientError(lastErr) {
			return lastErr
		}
		if err := c.wait(ctx, c.retryDelays[attempt]); err != nil {
			return err
		}
	}
	return lastErr
}

func (c *Client) do(ctx context.Context, method string, path string, query url.Values) (*http.Response, error) {
	return c.doWithBodyTimeout(ctx, c.timeout, method, path, query, nil)
}

func (c *Client) doWithBodyTimeout(ctx context.Context, timeout time.Duration, method string, path string, query url.Values, body io.Reader) (*http.Response, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for name, values := range OAuthHeaders(c.accessToken) {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("anthropic-beta", CCRBYOCBeta)
	if c.orgUUID != "" {
		request.Header.Set("x-organization-uuid", c.orgUUID)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	return response, nil
}

func (c *Client) endpoint(path string, query url.Values) string {
	endpoint := *c.baseURL
	basePath := strings.TrimRight(endpoint.Path, "/")
	path = "/" + strings.TrimLeft(path, "/")
	endpoint.Path = basePath + path
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func (c *Client) wait(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c.sleep(delay)
	return nil
}

func decodeGETResponse(response *http.Response, out any) (bool, error) {
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			return false, fmt.Errorf("decode sessions response: %w", err)
		}
		return false, nil
	}
	return response.StatusCode >= 500, responseError(response)
}

func transformCodeSession(session SessionResource) CodeSession {
	title := "Untitled"
	if session.Title != nil && *session.Title != "" {
		title = *session.Title
	}
	return CodeSession{
		ID:          session.ID,
		Title:       title,
		Description: "",
		Status:      session.SessionStatus,
		Repo:        repoFromSources(session.SessionContext.Sources),
		Turns:       []string{},
		CreatedAt:   session.CreatedAt,
		UpdatedAt:   session.UpdatedAt,
	}
}

func repoFromSources(sources []SessionContextSource) *Repo {
	for _, source := range sources {
		if source.Type != "git_repository" || source.URL == "" {
			continue
		}
		owner, name, ok := parseGitHubRepository(source.URL)
		if !ok {
			continue
		}
		repo := &Repo{Name: name, Owner: RepoOwner{Login: owner}}
		if source.Revision != nil {
			repo.DefaultBranch = *source.Revision
		}
		return repo
	}
	return nil
}

func parseGitHubRepository(rawURL string) (string, string, bool) {
	trimmed := strings.TrimSpace(rawURL)
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if after, ok := strings.CutPrefix(trimmed, "git@github.com:"); ok {
		path := after
		return splitGitHubPath(path)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || !strings.EqualFold(parsed.Host, "github.com") {
		return "", "", false
	}
	return splitGitHubPath(parsed.Path)
}

func splitGitHubPath(path string) (string, string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func randomUUID() (string, error) {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return "", fmt.Errorf("generate event uuid: %w", err)
	}
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16]), nil
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
		Retryable:  response.StatusCode >= 500,
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

func responseRequestID(response *http.Response) string {
	if response == nil {
		return ""
	}
	if requestID := response.Header.Get("request-id"); requestID != "" {
		return requestID
	}
	return response.Header.Get("x-request-id")
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
	if netErr, ok := errors.AsType[net.Error](err); ok {
		kind := core.APIErrorNetwork
		if netErr.Timeout() {
			kind = core.APIErrorTimeout
		}
		return &core.APIError{Kind: kind, Message: err.Error(), Retryable: true, Cause: err}
	}
	return &core.APIError{Kind: core.APIErrorNetwork, Message: err.Error(), Retryable: true, Cause: err}
}

func isTransientError(err error) bool {
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Kind == core.APIErrorNetwork || apiErr.Kind == core.APIErrorTimeout || apiErr.StatusCode >= 500
}
