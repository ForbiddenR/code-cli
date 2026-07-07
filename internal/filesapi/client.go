package filesapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"code-cli/internal/core"
)

// Client calls the Anthropic Files API.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
	oauthToken string
	maxRetries int
	baseDelay  time.Duration
	timeout    time.Duration
	sleep      func(time.Duration)
}

// NewClient creates a Files API client.
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
	maxRetries := config.MaxRetries
	if maxRetries <= 0 {
		maxRetries = DefaultMaxRetries
	}
	baseDelay := config.BaseDelay
	if baseDelay <= 0 {
		baseDelay = DefaultBaseDelay
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	sleep := config.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	return &Client{
		baseURL:    parsed,
		httpClient: httpClient,
		oauthToken: config.OAuthToken,
		maxRetries: maxRetries,
		baseDelay:  baseDelay,
		timeout:    timeout,
		sleep:      sleep,
	}, nil
}

// DownloadFile downloads one file's content.
func (c *Client) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	if strings.TrimSpace(fileID) == "" {
		return nil, fmt.Errorf("file id is required")
	}

	var content []byte
	err := c.retry(ctx, func(ctx context.Context) (bool, error) {
		response, err := c.do(ctx, http.MethodGet, "/v1/files/"+url.PathEscape(fileID)+"/content", nil, nil)
		if err != nil {
			return true, err
		}
		defer response.Body.Close()

		if response.StatusCode == http.StatusOK {
			content, err = io.ReadAll(response.Body)
			if err != nil {
				return false, fmt.Errorf("read file content: %w", err)
			}
			return false, nil
		}
		return retryableStatus(response.StatusCode), fileAPIError(response)
	})
	if err != nil {
		return nil, err
	}
	return content, nil
}

// ListFilesCreatedAfter lists files created after a timestamp, following pagination cursors.
func (c *Client) ListFilesCreatedAfter(ctx context.Context, afterCreatedAt string) ([]FileMetadata, error) {
	if strings.TrimSpace(afterCreatedAt) == "" {
		return nil, fmt.Errorf("after created at is required")
	}

	var all []FileMetadata
	var afterID string
	for {
		query := url.Values{}
		query.Set("after_created_at", afterCreatedAt)
		if afterID != "" {
			query.Set("after_id", afterID)
		}

		var page listFilesResponse
		err := c.retry(ctx, func(ctx context.Context) (bool, error) {
			response, err := c.do(ctx, http.MethodGet, "/v1/files", query, nil)
			if err != nil {
				return true, err
			}
			defer response.Body.Close()

			if response.StatusCode == http.StatusOK {
				if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
					return false, fmt.Errorf("decode files response: %w", err)
				}
				return false, nil
			}
			return retryableStatus(response.StatusCode), fileAPIError(response)
		})
		if err != nil {
			return nil, err
		}

		for _, file := range page.Data {
			all = append(all, FileMetadata{Filename: file.Filename, FileID: file.ID, Size: file.SizeBytes})
		}
		if !page.HasMore || len(page.Data) == 0 {
			break
		}
		afterID = page.Data[len(page.Data)-1].ID
		if afterID == "" {
			break
		}
	}
	return all, nil
}

func (c *Client) do(ctx context.Context, method string, path string, query url.Values, body io.Reader) (*http.Response, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("anthropic-version", AnthropicVersion)
	request.Header.Set("anthropic-beta", FilesAPIBetaHeader)
	if c.oauthToken != "" {
		request.Header.Set("Authorization", "Bearer "+c.oauthToken)
	}
	return c.httpClient.Do(request)
}

func (c *Client) endpoint(path string, query url.Values) string {
	endpoint := *c.baseURL
	basePath := strings.TrimRight(endpoint.Path, "/")
	path = "/" + strings.TrimLeft(path, "/")
	endpoint.Path = basePath + path
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func (c *Client) retry(ctx context.Context, operation func(context.Context) (bool, error)) error {
	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		retryable, err := operation(ctx)
		if err == nil || !retryable || attempt == c.maxRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		c.sleep(c.retryDelay(attempt))
	}
	return nil
}

func (c *Client) retryDelay(attempt int) time.Duration {
	delay := c.baseDelay * time.Duration(1<<(attempt-1))
	maxDelay := 8 * time.Second
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func fileAPIError(response *http.Response) *core.APIError {
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
	case http.StatusRequestEntityTooLarge:
		return core.APIErrorContextLength
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

type listFilesResponse struct {
	Data []struct {
		Filename  string `json:"filename"`
		ID        string `json:"id"`
		SizeBytes int64  `json:"size_bytes"`
	} `json:"data"`
	HasMore bool `json:"has_more"`
}
