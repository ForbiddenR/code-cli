// Package webfetch implements Claude Code's local WebFetchTool.
package webfetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf16"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
)

const (
	ToolName              = "WebFetch"
	DefaultMaxTokens      = 4096
	MaxURLLength          = 2000
	MaxHTTPContentLength  = 10 * 1024 * 1024
	MaxMarkdownLength     = 100000
	MaxRedirects          = 10
	FetchTimeout          = 60 * time.Second
	DomainCheckTimeout    = 10 * time.Second
	URLCacheTTL           = 15 * time.Minute
	URLCacheMaxWeight     = 50 * 1024 * 1024
	DomainCacheTTL        = 5 * time.Minute
	DomainCacheMaxEntries = 128
	defaultDomainInfoURL  = "https://api.anthropic.com/api/web/domain_info"
	defaultUserAgent      = "Claude-User (Claude Code; +https://support.anthropic.com/)"
)

type Input struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

type Output struct {
	Bytes      int    `json:"bytes"`
	Code       int    `json:"code"`
	CodeText   string `json:"codeText"`
	Result     string `json:"result"`
	DurationMs int64  `json:"durationMs"`
	URL        string `json:"url"`
}

type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id"`
	Type      string `json:"type"`
	Content   string `json:"content"`
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type MessageCreator interface {
	CreateMessage(context.Context, anthropicapi.MessageRequest, ...anthropicapi.CallOption) (*anthropicapi.MessageResponse, error)
}

type HTMLConverter interface {
	Convert(string) (string, error)
}

type BinaryPersister interface {
	Persist(context.Context, string, string, []byte) (string, error)
}

type Config struct {
	Client          MessageCreator
	HTTPClient      HTTPDoer
	PreflightClient HTTPDoer
	Converter       HTMLConverter
	Persister       BinaryPersister
	SmallModel      core.ModelID
	MaxTokens       int
	UserAgent       string
	DomainInfoURL   string
	SkipPreflight   bool
	Now             func() time.Time
	URLCache        *ContentCache
	DomainCache     *AllowedDomainCache
}

func DefaultConfig(client anthropicapi.Client) Config {
	return Config{Client: client}
}

type WebFetchTool struct {
	config Config
}

func New(config Config) *WebFetchTool {
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if client, ok := config.HTTPClient.(*http.Client); ok {
		copy := *client
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		config.HTTPClient = &copy
	}
	if config.PreflightClient == nil {
		config.PreflightClient = config.HTTPClient
	}
	if config.Converter == nil {
		config.Converter = defaultHTMLConverter{}
	}
	if config.SmallModel == "" {
		config.SmallModel = core.ModelClaudeHaiku45
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = DefaultMaxTokens
	}
	if config.UserAgent == "" {
		config.UserAgent = defaultUserAgent
	}
	if config.DomainInfoURL == "" {
		config.DomainInfoURL = defaultDomainInfoURL
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.URLCache == nil {
		config.URLCache = NewContentCache(URLCacheTTL, URLCacheMaxWeight, config.Now)
	}
	if config.DomainCache == nil {
		config.DomainCache = NewAllowedDomainCache(DomainCacheTTL, DomainCacheMaxEntries, config.Now)
	}
	return &WebFetchTool{config: config}
}

type ValidationResult struct {
	Valid     bool
	Message   string
	ErrorCode int
}

func ValidateInput(input Input) ValidationResult {
	if _, err := validateURL(input.URL); err != nil {
		return ValidationResult{Message: fmt.Sprintf("Error: Invalid URL %q. The URL provided could not be parsed.", input.URL), ErrorCode: 1}
	}
	return ValidationResult{Valid: true}
}

func validateURL(raw string) (*url.URL, error) {
	if len(utf16.Encode([]rune(raw))) > MaxURLLength {
		return nil, errors.New("Invalid URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return nil, errors.New("Invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("Invalid URL")
	}
	if parsed.User != nil {
		return nil, errors.New("Invalid URL")
	}
	if len(strings.Split(parsed.Hostname(), ".")) < 2 {
		return nil, errors.New("Invalid URL")
	}
	return parsed, nil
}

func (t *WebFetchTool) Call(ctx context.Context, input Input) (Output, error) {
	if t == nil {
		return Output{}, errors.New("web fetch tool is nil")
	}
	parsed, err := validateURL(input.URL)
	if err != nil {
		return Output{}, err
	}
	start := t.config.Now()

	content, ok := t.config.URLCache.Get(input.URL)
	if !ok {
		fetchURL := cloneURL(parsed)
		upgradeHTTPURL(fetchURL)
		if !t.config.SkipPreflight {
			if err := t.checkDomain(ctx, fetchURL.Hostname()); err != nil {
				return Output{}, err
			}
		}
		content, err = t.fetch(ctx, fetchURL, input.Prompt)
		if err != nil {
			return Output{}, err
		}
		if !content.Redirect {
			t.config.URLCache.Set(input.URL, content)
		}
	}

	result := content.Content
	if !content.Redirect && !(isPreapprovedURL(parsed) && strings.Contains(content.ContentType, "text/markdown") && jsStringLength(content.Content) < MaxMarkdownLength) {
		result, err = t.applyPrompt(ctx, content.Content, input.Prompt, isPreapprovedURL(parsed))
		if err != nil {
			return Output{}, err
		}
	}
	if content.PersistedPath != "" {
		result += fmt.Sprintf("\n\n[Binary content (%s, %s) also saved to %s]", content.ContentType, formatFileSize(content.PersistedSize), content.PersistedPath)
	}

	duration := max(t.config.Now().Sub(start).Milliseconds(), 0)
	return Output{
		Bytes:      content.Bytes,
		Code:       content.Code,
		CodeText:   content.CodeText,
		Result:     result,
		DurationMs: duration,
		URL:        input.URL,
	}, nil
}

func (t *WebFetchTool) ClearCache() {
	if t == nil {
		return
	}
	t.config.URLCache.Clear()
	t.config.DomainCache.Clear()
}

func MapToolResultToToolResultBlockParam(output Output, toolUseID string) ToolResultBlock {
	return ToolResultBlock{ToolUseID: toolUseID, Type: "tool_result", Content: output.Result}
}

func cloneURL(value *url.URL) *url.URL {
	copy := *value
	return &copy
}

func upgradeHTTPURL(value *url.URL) {
	if value.Scheme != "http" {
		return
	}
	value.Scheme = "https"
	if value.Port() == "80" || value.Port() == "443" {
		value.Host = value.Hostname()
	}
}
