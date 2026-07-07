package anthropicapi

import (
	"context"
	"net/http"

	"code-cli/internal/core"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const betaHeader = "anthropic-beta"

// SDKClient implements Client using the official Anthropic Go SDK.
type SDKClient struct {
	client anthropic.Client
	config core.APIConfig
}

// NewSDKClient creates an SDK-backed Claude API client.
func NewSDKClient(config core.APIConfig) (*SDKClient, error) {
	config = config.WithDefaults()

	options := []option.RequestOption{
		option.WithBaseURL(config.BaseURL),
		// Retry behavior is owned by this package so it stays normalized and testable.
		option.WithMaxRetries(0),
	}
	if config.APIKey != "" {
		options = append(options, option.WithAPIKey(config.APIKey))
	}
	if config.UserAgent != "" {
		options = append(options, option.WithHeader("user-agent", config.UserAgent))
	}
	for name, value := range config.DefaultHeaders {
		options = append(options, option.WithHeader(name, value))
	}
	for _, beta := range config.Betas {
		if beta != "" {
			options = append(options, option.WithHeaderAdd(betaHeader, beta))
		}
	}

	client := anthropic.NewClient(options...)
	return &SDKClient{client: client, config: config}, nil
}

// CreateMessage sends a non-streaming Messages API request.
func (c *SDKClient) CreateMessage(ctx context.Context, req MessageRequest, opts ...CallOption) (*MessageResponse, error) {
	params, err := newMessageParams(req)
	if err != nil {
		return nil, err
	}

	callOptions := ApplyOptions(opts...)
	var raw *http.Response
	message, err := retryAPI(ctx, c.retryConfig(callOptions), nil, func(ctx context.Context, _ int) (*anthropic.Message, error) {
		raw = nil
		requestOptions := append(c.requestOptions(req.Betas, callOptions), option.WithResponseInto(&raw))
		return c.client.Messages.New(ctx, params, requestOptions...)
	})
	if err != nil {
		return nil, err
	}

	response, err := normalizeMessage(message)
	if err != nil {
		return nil, err
	}
	response.RequestID = responseRequestID(raw)
	return response, nil
}

// StreamMessage sends a streaming Messages API request.
func (c *SDKClient) StreamMessage(ctx context.Context, req MessageRequest, opts ...CallOption) (Stream, error) {
	params, err := newMessageParams(req)
	if err != nil {
		return nil, err
	}

	callOptions := ApplyOptions(opts...)
	stream, err := retryAPI[Stream](ctx, c.retryConfig(callOptions), nil, func(ctx context.Context, _ int) (Stream, error) {
		streamCtx, cancel := context.WithCancel(ctx)
		sdkStream := c.client.Messages.NewStreaming(streamCtx, params, c.requestOptions(req.Betas, callOptions)...)
		if err := sdkStream.Err(); err != nil {
			cancel()
			_ = sdkStream.Close()
			return nil, err
		}
		return newSDKStream(streamCtx, cancel, sdkStream), nil
	})
	if err != nil {
		return nil, err
	}
	return stream, nil
}

// CountTokens sends a Messages API token-counting request.
func (c *SDKClient) CountTokens(ctx context.Context, req TokenCountRequest, opts ...CallOption) (*TokenCountResponse, error) {
	params, err := newTokenCountParams(req)
	if err != nil {
		return nil, err
	}

	callOptions := ApplyOptions(opts...)
	var raw *http.Response
	count, err := retryAPI(ctx, c.retryConfig(callOptions), nil, func(ctx context.Context, _ int) (*anthropic.MessageTokensCount, error) {
		raw = nil
		requestOptions := append(c.requestOptions(req.Betas, callOptions), option.WithResponseInto(&raw))
		return c.client.Messages.CountTokens(ctx, params, requestOptions...)
	})
	if err != nil {
		return nil, err
	}

	return &TokenCountResponse{
		InputTokens: count.InputTokens,
		RequestID:   responseRequestID(raw),
	}, nil
}

func (c *SDKClient) retryConfig(callOptions CallOptions) core.RetryConfig {
	if callOptions.Retry != nil {
		return callOptions.Retry.WithDefaults()
	}
	if c.config.Retry != nil {
		return c.config.Retry.WithDefaults()
	}
	return core.DefaultRetryConfig()
}

func (c *SDKClient) requestOptions(betas []string, callOptions CallOptions) []option.RequestOption {
	requestOptions := make([]option.RequestOption, 0, len(betas)+len(callOptions.Betas)+len(callOptions.Headers)+1)

	if callOptions.Timeout > 0 {
		requestOptions = append(requestOptions, option.WithRequestTimeout(callOptions.Timeout))
	}
	for name, value := range callOptions.Headers {
		requestOptions = append(requestOptions, option.WithHeader(name, value))
	}
	for _, beta := range betas {
		if beta != "" {
			requestOptions = append(requestOptions, option.WithHeaderAdd(betaHeader, beta))
		}
	}
	for _, beta := range callOptions.Betas {
		if beta != "" {
			requestOptions = append(requestOptions, option.WithHeaderAdd(betaHeader, beta))
		}
	}

	return requestOptions
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
