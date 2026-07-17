package teleportapi

import (
	"context"
	"net/http"
	"time"

	"code-cli/internal/sessionsapi"
	"code-cli/internal/teleportauth"
)

// Client prepares Teleport OAuth auth and delegates to the Sessions API client.
type Client struct {
	baseURL          string
	httpClient       *http.Client
	timeout          time.Duration
	sendEventTimeout time.Duration
	retryDelays      []time.Duration
	sleep            func(time.Duration)
	preparer         *teleportauth.Preparer
}

// Config contains process-level settings for authenticated Teleport API calls.
type Config struct {
	BaseURL          string
	HTTPClient       *http.Client
	Timeout          time.Duration
	SendEventTimeout time.Duration
	RetryDelays      []time.Duration
	Sleep            func(time.Duration)
	Preparer         *teleportauth.Preparer
}

// NewClient creates an authenticated Teleport API helper.
func NewClient(config Config) *Client {
	return &Client{
		baseURL:          config.BaseURL,
		httpClient:       config.HTTPClient,
		timeout:          config.Timeout,
		sendEventTimeout: config.SendEventTimeout,
		retryDelays:      append([]time.Duration(nil), config.RetryDelays...),
		sleep:            config.Sleep,
		preparer:         config.Preparer,
	}
}

// ListCodeSessions prepares auth and fetches transformed code sessions.
func (c *Client) ListCodeSessions(ctx context.Context) ([]sessionsapi.CodeSession, error) {
	client, err := c.sessionsClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ListCodeSessions(ctx)
}

// FetchSession prepares auth and fetches a raw session resource.
func (c *Client) FetchSession(ctx context.Context, sessionID string) (sessionsapi.SessionResource, error) {
	client, err := c.sessionsClient(ctx)
	if err != nil {
		return sessionsapi.SessionResource{}, err
	}
	return client.FetchSession(ctx, sessionID)
}

// SendEventToRemoteSession prepares auth and sends one user event to a remote session.
func (c *Client) SendEventToRemoteSession(ctx context.Context, sessionID string, messageContent sessionsapi.RemoteMessageContent, opts sessionsapi.SendEventOptions) (bool, error) {
	client, err := c.sessionsClient(ctx)
	if err != nil {
		return false, err
	}
	return client.SendEventToRemoteSession(ctx, sessionID, messageContent, opts)
}

// UpdateSessionTitle prepares auth and updates a remote session title.
func (c *Client) UpdateSessionTitle(ctx context.Context, sessionID string, title string) (bool, error) {
	client, err := c.sessionsClient(ctx)
	if err != nil {
		return false, err
	}
	return client.UpdateSessionTitle(ctx, sessionID, title)
}

func (c *Client) sessionsClient(ctx context.Context) (*sessionsapi.Client, error) {
	prepared, err := c.prepare(ctx)
	if err != nil {
		return nil, err
	}
	return sessionsapi.NewClient(sessionsapi.Config{
		BaseURL:          c.baseURL,
		AccessToken:      prepared.AccessToken,
		OrgUUID:          prepared.OrgUUID,
		HTTPClient:       c.httpClient,
		Timeout:          c.timeout,
		SendEventTimeout: c.sendEventTimeout,
		RetryDelays:      append([]time.Duration(nil), c.retryDelays...),
		Sleep:            c.sleep,
	})
}

func (c *Client) prepare(ctx context.Context) (teleportauth.PreparedRequest, error) {
	if c.preparer == nil {
		return teleportauth.PreparedRequest{}, teleportauth.ErrMissingPreparer
	}
	return c.preparer.PrepareAPIRequest(ctx)
}

// PollRemoteSessionEvents prepares auth and polls remote session events.
func (c *Client) PollRemoteSessionEvents(ctx context.Context, sessionID string, opts sessionsapi.PollEventsOptions) (sessionsapi.PollEventsResult, error) {
	client, err := c.sessionsClient(ctx)
	if err != nil {
		return sessionsapi.PollEventsResult{}, err
	}
	return client.PollRemoteSessionEvents(ctx, sessionID, opts)
}

// ArchiveRemoteSession prepares auth and archives a remote session.
func (c *Client) ArchiveRemoteSession(ctx context.Context, sessionID string) (bool, error) {
	client, err := c.sessionsClient(ctx)
	if err != nil {
		return false, err
	}
	return client.ArchiveRemoteSession(ctx, sessionID)
}
