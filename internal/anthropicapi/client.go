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

	options := []option.RequestOption{option.WithBaseURL(config.BaseURL)}
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

	var raw *http.Response
	requestOptions := append(c.requestOptions(req.Betas, opts...), option.WithResponseInto(&raw))

	message, err := c.client.Messages.New(ctx, params, requestOptions...)
	if err != nil {
		return nil, ClassifyError(err)
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

	streamCtx, cancel := context.WithCancel(ctx)
	stream := c.client.Messages.NewStreaming(streamCtx, params, c.requestOptions(req.Betas, opts...)...)
	if err := stream.Err(); err != nil {
		cancel()
		_ = stream.Close()
		return nil, ClassifyError(err)
	}

	return newSDKStream(streamCtx, cancel, stream), nil
}

// CountTokens sends a Messages API token-counting request.
func (c *SDKClient) CountTokens(ctx context.Context, req TokenCountRequest, opts ...CallOption) (*TokenCountResponse, error) {
	params, err := newTokenCountParams(req)
	if err != nil {
		return nil, err
	}

	var raw *http.Response
	requestOptions := append(c.requestOptions(req.Betas, opts...), option.WithResponseInto(&raw))

	count, err := c.client.Messages.CountTokens(ctx, params, requestOptions...)
	if err != nil {
		return nil, ClassifyError(err)
	}

	return &TokenCountResponse{
		InputTokens: count.InputTokens,
		RequestID:   responseRequestID(raw),
	}, nil
}

func (c *SDKClient) requestOptions(betas []string, options ...CallOption) []option.RequestOption {
	callOptions := ApplyOptions(options...)
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
