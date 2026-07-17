package environments

import (
	"bytes"
	"context"
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
	"code-cli/internal/sessionsapi"
	"code-cli/internal/teleportauth"
)

const (
	// DefaultBaseURL is the production OAuth BASE_API_URL used by environment-provider endpoints.
	DefaultBaseURL = sessionsapi.DefaultBaseURL
	// DefaultTimeout matches the 15-second timeout in teleport/environments.ts.
	DefaultTimeout = 15 * time.Second
	// CCRBYOCBeta is required by the default cloud environment creation endpoint.
	CCRBYOCBeta = sessionsapi.CCRBYOCBeta
)

const (
	KindAnthropicCloud EnvironmentKind  = "anthropic_cloud"
	KindBYOC           EnvironmentKind  = "byoc"
	KindBridge         EnvironmentKind  = "bridge"
	StateActive        EnvironmentState = "active"
)

// EnvironmentKind is the environment provider kind returned by the API.
type EnvironmentKind string

// EnvironmentState is the environment provider lifecycle state returned by the API.
type EnvironmentState string

// EnvironmentResource is one environment provider resource.
type EnvironmentResource struct {
	Kind          EnvironmentKind  `json:"kind"`
	EnvironmentID string           `json:"environment_id"`
	Name          string           `json:"name"`
	CreatedAt     string           `json:"created_at"`
	State         EnvironmentState `json:"state"`
}

// ListResponse is the raw response from GET /v1/environment_providers.
type ListResponse struct {
	Environments []EnvironmentResource `json:"environments"`
	HasMore      bool                  `json:"has_more"`
	FirstID      *string               `json:"first_id"`
	LastID       *string               `json:"last_id"`
}

// CreateDefaultCloudRequest is the TypeScript-compatible cloud environment creation body.
type CreateDefaultCloudRequest struct {
	Name        string                 `json:"name"`
	Kind        EnvironmentKind        `json:"kind"`
	Description string                 `json:"description"`
	Config      CloudEnvironmentConfig `json:"config"`
}

// CloudEnvironmentConfig is the default anthropic_cloud config body sent by the TypeScript client.
type CloudEnvironmentConfig struct {
	EnvironmentType string            `json:"environment_type"`
	CWD             string            `json:"cwd"`
	InitScript      *string           `json:"init_script"`
	Environment     map[string]string `json:"environment"`
	Languages       []Language        `json:"languages"`
	NetworkConfig   NetworkConfig     `json:"network_config"`
}

// Language describes one preinstalled language for the default cloud environment.
type Language struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// NetworkConfig describes default cloud environment network access.
type NetworkConfig struct {
	AllowedHosts      []string `json:"allowed_hosts"`
	AllowDefaultHosts bool     `json:"allow_default_hosts"`
}

// Config contains process-level settings for environment-provider API calls.
type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
	Preparer   *teleportauth.Preparer
}

// Client calls the CCR environment-provider API.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
	timeout    time.Duration
	preparer   *teleportauth.Preparer
}

// NewClient creates an environment-provider API client.
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
	return &Client{baseURL: parsed, httpClient: httpClient, timeout: timeout, preparer: config.Preparer}, nil
}

// FetchEnvironments fetches the list of available environment providers.
func (c *Client) FetchEnvironments(ctx context.Context) ([]EnvironmentResource, error) {
	var response ListResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/environment_providers", nil, false, &response); err != nil {
		return nil, fmt.Errorf("failed to fetch environments: %w", err)
	}
	return append([]EnvironmentResource(nil), response.Environments...), nil
}

// CreateDefaultCloudEnvironment creates a default anthropic_cloud environment.
func (c *Client) CreateDefaultCloudEnvironment(ctx context.Context, name string) (EnvironmentResource, error) {
	if strings.TrimSpace(name) == "" {
		return EnvironmentResource{}, fmt.Errorf("environment name is required")
	}
	request := DefaultCloudEnvironmentRequest(name)
	body, err := json.Marshal(request)
	if err != nil {
		return EnvironmentResource{}, fmt.Errorf("marshal environment create request: %w", err)
	}
	var environment EnvironmentResource
	if err := c.doJSON(ctx, http.MethodPost, "/v1/environment_providers/cloud/create", bytes.NewReader(body), true, &environment); err != nil {
		return EnvironmentResource{}, err
	}
	return environment, nil
}

// DefaultCloudEnvironmentRequest returns the TypeScript-compatible default cloud creation body.
func DefaultCloudEnvironmentRequest(name string) CreateDefaultCloudRequest {
	return CreateDefaultCloudRequest{
		Name:        name,
		Kind:        KindAnthropicCloud,
		Description: "",
		Config: CloudEnvironmentConfig{
			EnvironmentType: "anthropic",
			CWD:             "/home/user",
			InitScript:      nil,
			Environment:     map[string]string{},
			Languages: []Language{
				{Name: "python", Version: "3.11"},
				{Name: "node", Version: "20"},
			},
			NetworkConfig: NetworkConfig{AllowedHosts: []string{}, AllowDefaultHosts: true},
		},
	}
}

func (c *Client) doJSON(ctx context.Context, method string, path string, body io.Reader, includeBeta bool, out any) error {
	prepared, err := c.prepare(ctx)
	if err != nil {
		return err
	}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	for name, values := range sessionsapi.OAuthHeaders(prepared.AccessToken) {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("x-organization-uuid", prepared.OrgUUID)
	if includeBeta {
		request.Header.Set("anthropic-beta", CCRBYOCBeta)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return classifyTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseError(response)
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("decode environment response: %w", err)
	}
	return nil
}

func (c *Client) prepare(ctx context.Context) (teleportauth.PreparedRequest, error) {
	if c.preparer == nil {
		return teleportauth.PreparedRequest{}, teleportauth.ErrMissingPreparer
	}
	return c.preparer.PrepareAPIRequest(ctx)
}

func (c *Client) endpoint(path string) string {
	endpoint := *c.baseURL
	basePath := strings.TrimRight(endpoint.Path, "/")
	path = "/" + strings.TrimLeft(path, "/")
	endpoint.Path = basePath + path
	return endpoint.String()
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
	return &core.APIError{Kind: errorKindForStatus(response.StatusCode), StatusCode: response.StatusCode, Message: message, RequestID: responseRequestID(response), Retryable: response.StatusCode >= 500}
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
