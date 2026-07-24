package anthropicapi

import (
	"context"
	"time"

	"code-cli/internal/core"
)

const DefaultMaxTokens = 4096

// Client is the API boundary consumed by the future query engine.
type Client interface {
	CreateMessage(ctx context.Context, req MessageRequest, opts ...CallOption) (*MessageResponse, error)
	StreamMessage(ctx context.Context, req MessageRequest, opts ...CallOption) (Stream, error)
	CountTokens(ctx context.Context, req TokenCountRequest, opts ...CallOption) (*TokenCountResponse, error)
}

// MessageRequest is the normalized input for a Claude Messages API call.
type MessageRequest struct {
	Model         core.ModelID           `json:"model"`
	MaxTokens     int                    `json:"max_tokens"`
	System        []core.SystemBlock     `json:"system,omitempty"`
	Messages      []core.Message         `json:"messages"`
	Tools         []core.ToolDefinition  `json:"tools,omitempty"`
	ServerTools   []ServerToolDefinition `json:"server_tools,omitempty"`
	ToolChoice    *ToolChoice            `json:"tool_choice,omitempty"`
	Thinking      *core.ThinkingConfig   `json:"thinking,omitempty"`
	OutputConfig  *core.OutputConfig     `json:"output_config,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Metadata      map[string]string      `json:"metadata,omitempty"`
	Betas         []string               `json:"betas,omitempty"`
}

// ServerToolDefinition describes a provider-executed tool exposed by the
// Messages API. Only the web-search variant is currently supported.
type ServerToolDefinition struct {
	Type           string   `json:"type"`
	Name           string   `json:"name"`
	MaxUses        int      `json:"max_uses,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
}

const ServerToolWebSearch20250305 = "web_search_20250305"

// ToolChoice constrains the model's tool selection for a Messages API call.
type ToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// WithDefaults returns a copy of the request with API-boundary defaults applied.
func (r MessageRequest) WithDefaults() MessageRequest {
	if r.Model == "" {
		r.Model = core.DefaultModel
	}
	if r.MaxTokens == 0 {
		r.MaxTokens = DefaultMaxTokens
	}
	return r
}

// MessageResponse is the normalized result of a non-streaming or completed streaming call.
type MessageResponse struct {
	ID           string              `json:"id"`
	Model        core.ModelID        `json:"model"`
	Role         core.Role           `json:"role"`
	Content      []core.ContentBlock `json:"content"`
	StopReason   core.StopReason     `json:"stop_reason,omitempty"`
	StopSequence string              `json:"stop_sequence,omitempty"`
	Usage        core.Usage          `json:"usage"`
	RequestID    string              `json:"request_id,omitempty"`
}

// TokenCountRequest is the normalized input for token counting.
type TokenCountRequest struct {
	Model        core.ModelID          `json:"model"`
	System       []core.SystemBlock    `json:"system,omitempty"`
	Messages     []core.Message        `json:"messages"`
	Tools        []core.ToolDefinition `json:"tools,omitempty"`
	Thinking     *core.ThinkingConfig  `json:"thinking,omitempty"`
	OutputConfig *core.OutputConfig    `json:"output_config,omitempty"`
	Betas        []string              `json:"betas,omitempty"`
}

// TokenCountResponse is the normalized result of token counting.
type TokenCountResponse struct {
	InputTokens int64  `json:"input_tokens"`
	RequestID   string `json:"request_id,omitempty"`
}

// Stream is an active stream of normalized Claude events.
type Stream interface {
	Events() <-chan StreamEvent
	Close() error
}

// StreamEventType identifies normalized streaming events.
type StreamEventType string

const (
	StreamEventMessageStart      StreamEventType = "message_start"
	StreamEventContentBlockStart StreamEventType = "content_block_start"
	StreamEventContentBlockDelta StreamEventType = "content_block_delta"
	StreamEventContentBlockStop  StreamEventType = "content_block_stop"
	StreamEventMessageDelta      StreamEventType = "message_delta"
	StreamEventMessageStop       StreamEventType = "message_stop"
	StreamEventError             StreamEventType = "error"
)

// StreamEvent is the stable event shape exposed above raw SDK or SSE events.
type StreamEvent struct {
	Type         StreamEventType    `json:"type"`
	Message      *MessageResponse   `json:"message,omitempty"`
	Index        int                `json:"index,omitempty"`
	Block        *core.ContentBlock `json:"block,omitempty"`
	Delta        *ContentDelta      `json:"delta,omitempty"`
	MessageDelta *MessageDelta      `json:"message_delta,omitempty"`
	Usage        *core.Usage        `json:"usage,omitempty"`
	Error        error              `json:"-"`
}

// ContentDelta is normalized partial content from a streaming response.
type ContentDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	Signature   string `json:"signature,omitempty"`
}

// MessageDelta is normalized message-level stream metadata.
type MessageDelta struct {
	StopReason   core.StopReason `json:"stop_reason,omitempty"`
	StopSequence string          `json:"stop_sequence,omitempty"`
}

// CallOptions controls one API call without changing the process-level config.
type CallOptions struct {
	Timeout time.Duration
	Betas   []string
	Headers map[string]string
	Retry   *core.RetryConfig
}

// CallOption mutates call-level options.
type CallOption func(*CallOptions)

// WithTimeout sets a call-specific timeout.
func WithTimeout(timeout time.Duration) CallOption {
	return func(opts *CallOptions) {
		opts.Timeout = timeout
	}
}

// WithBeta appends one beta header value for a call.
func WithBeta(beta string) CallOption {
	return func(opts *CallOptions) {
		opts.Betas = append(opts.Betas, beta)
	}
}

// WithHeader adds one call-specific header.
func WithHeader(name string, value string) CallOption {
	return func(opts *CallOptions) {
		if opts.Headers == nil {
			opts.Headers = map[string]string{}
		}
		opts.Headers[name] = value
	}
}

// WithRetryConfig sets a call-specific retry policy.
func WithRetryConfig(config core.RetryConfig) CallOption {
	return func(opts *CallOptions) {
		retry := config
		opts.Retry = &retry
	}
}

// WithoutRetries disables retries for one call.
func WithoutRetries() CallOption {
	return WithRetryConfig(core.RetryConfig{MaxRetries: 0})
}

// ApplyOptions returns concrete call options from functional options.
func ApplyOptions(options ...CallOption) CallOptions {
	var opts CallOptions
	for _, option := range options {
		if option != nil {
			option(&opts)
		}
	}
	return opts
}
