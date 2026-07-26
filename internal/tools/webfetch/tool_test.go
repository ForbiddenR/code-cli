package webfetch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

type fakeMessageCreator struct {
	request  anthropicapi.MessageRequest
	calls    int
	response *anthropicapi.MessageResponse
	err      error
}

func (f *fakeMessageCreator) CreateMessage(_ context.Context, request anthropicapi.MessageRequest, _ ...anthropicapi.CallOption) (*anthropicapi.MessageResponse, error) {
	f.calls++
	f.request = request
	return f.response, f.err
}

type converterFunc func(string) (string, error)

func (f converterFunc) Convert(input string) (string, error) { return f(input) }

type persisterFunc func(context.Context, string, string, []byte) (string, error)

func (f persisterFunc) Persist(ctx context.Context, name, contentType string, data []byte) (string, error) {
	return f(ctx, name, contentType, data)
}

func response(code int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode:    code,
		Status:        http.StatusText(code),
		Header:        http.Header{"Content-Type": []string{contentType}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func modelResponse(text string) *anthropicapi.MessageResponse {
	return &anthropicapi.MessageResponse{Content: []core.ContentBlock{core.TextBlock(text)}}
}

func TestValidateInput(t *testing.T) {
	tests := []struct {
		name  string
		url   string
		valid bool
	}{
		{name: "valid", url: "https://example.com/path", valid: true},
		{name: "invalid", url: "not a URL"},
		{name: "credentials", url: "https://user:pass@example.com"},
		{name: "single label", url: "https://localhost"},
		{name: "unsupported scheme", url: "ftp://example.com/file"},
		{name: "too long", url: "https://example.com/" + strings.Repeat("a", MaxURLLength)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ValidateInput(Input{URL: test.url})
			if got.Valid != test.valid {
				t.Fatalf("ValidateInput() = %#v", got)
			}
		})
	}
}

func TestCallFetchesLocallyAndAppliesPrompt(t *testing.T) {
	var fetched *http.Request
	httpClient := doerFunc(func(request *http.Request) (*http.Response, error) {
		fetched = request
		return response(http.StatusOK, "text/plain", "local body"), nil
	})
	client := &fakeMessageCreator{response: modelResponse("processed")}
	tool := New(Config{Client: client, HTTPClient: httpClient, SkipPreflight: true, UserAgent: "test-agent"})

	got, err := tool.Call(context.Background(), Input{URL: "http://example.com/page", Prompt: "Summarize"})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if fetched.URL.String() != "https://example.com/page" {
		t.Fatalf("fetch URL = %q", fetched.URL)
	}
	if fetched.Header.Get("Accept") != "text/markdown, text/html, */*" || fetched.Header.Get("User-Agent") != "test-agent" {
		t.Fatalf("headers = %#v", fetched.Header)
	}
	if got.Result != "processed" || got.Bytes != len("local body") || got.Code != 200 || got.CodeText != "OK" || got.URL != "http://example.com/page" {
		t.Fatalf("output = %#v", got)
	}
	if client.request.Model != core.ModelClaudeHaiku45 || client.request.Thinking != nil || len(client.request.Tools) != 0 || len(client.request.ServerTools) != 0 {
		t.Fatalf("model request = %#v", client.request)
	}
	prompt := client.request.Messages[0].Content[0].Text
	if !strings.Contains(prompt, "local body") || !strings.Contains(prompt, "Summarize") || !strings.Contains(prompt, restrictedGuidelines) {
		t.Fatalf("secondary prompt = %q", prompt)
	}
}

func TestPreflightCachesAllowedDomain(t *testing.T) {
	preflightCalls := 0
	preflight := doerFunc(func(request *http.Request) (*http.Response, error) {
		preflightCalls++
		if request.URL.Query().Get("domain") != "example.com" {
			t.Fatalf("domain query = %q", request.URL.RawQuery)
		}
		return response(http.StatusOK, "application/json", `{"can_fetch":true}`), nil
	})
	fetchCalls := 0
	fetch := doerFunc(func(*http.Request) (*http.Response, error) {
		fetchCalls++
		return response(http.StatusOK, "text/plain", "body"), nil
	})
	client := &fakeMessageCreator{response: modelResponse("result")}
	tool := New(Config{Client: client, HTTPClient: fetch, PreflightClient: preflight, DomainInfoURL: "https://preflight.test/domain"})

	for range 2 {
		if _, err := tool.Call(context.Background(), Input{URL: "https://example.com", Prompt: "p"}); err != nil {
			t.Fatalf("Call() error = %v", err)
		}
	}
	if preflightCalls != 1 || fetchCalls != 1 || client.calls != 2 {
		t.Fatalf("calls: preflight=%d fetch=%d model=%d", preflightCalls, fetchCalls, client.calls)
	}
}

func TestPreflightBlockedAndFailed(t *testing.T) {
	for _, test := range []struct {
		name string
		doer doerFunc
		want string
	}{
		{name: "blocked", doer: func(*http.Request) (*http.Response, error) {
			return response(200, "application/json", `{"can_fetch":false}`), nil
		}, want: "Claude Code is unable to fetch from example.com"},
		{name: "failed", doer: func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }, want: "Unable to verify if domain example.com is safe to fetch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool := New(Config{PreflightClient: test.doer, HTTPClient: test.doer})
			_, err := tool.Call(context.Background(), Input{URL: "https://example.com"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Call() error = %v", err)
			}
		})
	}
}

func TestPreapprovedMarkdownBypassesModel(t *testing.T) {
	client := &fakeMessageCreator{err: errors.New("must not be called")}
	tool := New(Config{
		Client:        client,
		SkipPreflight: true,
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			return response(200, "text/markdown; charset=utf-8", "# Go"), nil
		}),
	})
	got, err := tool.Call(context.Background(), Input{URL: "https://go.dev/doc", Prompt: "ignored"})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got.Result != "# Go" || client.calls != 0 {
		t.Fatalf("result = %q, model calls = %d", got.Result, client.calls)
	}
}

func TestHTMLConversionAndURLCache(t *testing.T) {
	fetchCalls := 0
	convertCalls := 0
	client := &fakeMessageCreator{response: modelResponse("summary")}
	tool := New(Config{
		Client:        client,
		SkipPreflight: true,
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			fetchCalls++
			return response(200, "text/html", "<strong>Hello</strong>"), nil
		}),
		Converter: converterFunc(func(input string) (string, error) {
			convertCalls++
			return "**Hello**", nil
		}),
	})
	for range 2 {
		if _, err := tool.Call(context.Background(), Input{URL: "https://example.com", Prompt: "p"}); err != nil {
			t.Fatalf("Call() error = %v", err)
		}
	}
	if fetchCalls != 1 || convertCalls != 1 || client.calls != 2 {
		t.Fatalf("calls: fetch=%d convert=%d model=%d", fetchCalls, convertCalls, client.calls)
	}
	if !strings.Contains(client.request.Messages[0].Content[0].Text, "**Hello**") {
		t.Fatalf("model prompt = %q", client.request.Messages[0].Content[0].Text)
	}
}

func TestRedirectPolicies(t *testing.T) {
	t.Run("same host relative", func(t *testing.T) {
		calls := 0
		tool := New(Config{Client: &fakeMessageCreator{response: modelResponse("ok")}, SkipPreflight: true, HTTPClient: doerFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				result := response(302, "", "")
				result.Header.Set("Location", "/next")
				return result, nil
			}
			if request.URL.Path != "/next" {
				t.Fatalf("redirect path = %q", request.URL.Path)
			}
			return response(200, "text/plain", "body"), nil
		})})
		if _, err := tool.Call(context.Background(), Input{URL: "https://example.com/start", Prompt: "p"}); err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if calls != 2 {
			t.Fatalf("calls = %d", calls)
		}
	})

	t.Run("cross host", func(t *testing.T) {
		client := &fakeMessageCreator{err: errors.New("must not run")}
		tool := New(Config{Client: client, SkipPreflight: true, HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			result := response(301, "", "")
			result.Header.Set("Location", "https://other.example/path")
			return result, nil
		})})
		got, err := tool.Call(context.Background(), Input{URL: "https://example.com/start", Prompt: "inspect"})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if !strings.Contains(got.Result, "REDIRECT DETECTED") || !strings.Contains(got.Result, `- prompt: "inspect"`) || client.calls != 0 {
			t.Fatalf("output = %#v, model calls = %d", got, client.calls)
		}
	})
}

func TestInjectedHTTPClientDoesNotAutoFollowRedirects(t *testing.T) {
	targetCalls := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalls++
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", target.URL)
		writer.WriteHeader(http.StatusFound)
	}))
	defer origin.Close()

	tool := New(Config{SkipPreflight: true, HTTPClient: origin.Client()})
	got, err := tool.Call(context.Background(), Input{URL: origin.URL, Prompt: "p"})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if targetCalls != 0 || !strings.Contains(got.Result, "REDIRECT DETECTED") {
		t.Fatalf("target calls = %d, result = %q", targetCalls, got.Result)
	}
}

func TestResponseLimitsAndEgressError(t *testing.T) {
	t.Run("content length", func(t *testing.T) {
		tool := New(Config{SkipPreflight: true, HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			result := response(200, "text/plain", "")
			result.ContentLength = MaxHTTPContentLength + 1
			return result, nil
		})})
		_, err := tool.Call(context.Background(), Input{URL: "https://example.com"})
		if err == nil || !strings.Contains(err.Error(), "maximum size") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("egress", func(t *testing.T) {
		tool := New(Config{SkipPreflight: true, HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			result := response(403, "text/plain", "")
			result.Header.Set("X-Proxy-Error", "blocked-by-allowlist")
			return result, nil
		})})
		_, err := tool.Call(context.Background(), Input{URL: "https://example.com"})
		if err == nil || err.Error() != `{"error_type":"EGRESS_BLOCKED","domain":"example.com","message":"Access to example.com is blocked by the network egress proxy."}` {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestBinaryPersistence(t *testing.T) {
	var savedName, savedType string
	var saved []byte
	client := &fakeMessageCreator{response: modelResponse("binary summary")}
	tool := New(Config{
		Client:        client,
		SkipPreflight: true,
		HTTPClient:    doerFunc(func(*http.Request) (*http.Response, error) { return response(200, "application/pdf", "PDF"), nil }),
		Persister: persisterFunc(func(_ context.Context, name, contentType string, data []byte) (string, error) {
			savedName, savedType, saved = name, contentType, append([]byte(nil), data...)
			return "/tmp/result.pdf", nil
		}),
	})
	got, err := tool.Call(context.Background(), Input{URL: "https://example.com/file", Prompt: "p"})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if savedName != "webfetch.pdf" || savedType != "application/pdf" || string(saved) != "PDF" {
		t.Fatalf("saved = %q %q %q", savedName, savedType, saved)
	}
	if !strings.Contains(got.Result, "binary summary") || !strings.Contains(got.Result, "[Binary content (application/pdf, 3 bytes) also saved to /tmp/result.pdf]") {
		t.Fatalf("result = %q", got.Result)
	}
}

func TestDefaultHTMLConverter(t *testing.T) {
	got, err := (defaultHTMLConverter{}).Convert("<h1>Heading</h1><p><strong>bold</strong></p>")
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if !strings.Contains(got, "# Heading") || !strings.Contains(got, "**bold**") {
		t.Fatalf("Markdown = %q", got)
	}
}

func TestPromptTruncationAndPreapprovedPaths(t *testing.T) {
	content := strings.Repeat("a", MaxMarkdownLength+1)
	prompt := makeSecondaryModelPrompt(content, "p", false)
	if !strings.Contains(prompt, "[Content truncated due to length...]") || strings.Count(prompt, "a") < MaxMarkdownLength {
		t.Fatalf("truncated prompt length = %d", len(prompt))
	}
	if !isPreapprovedHost("github.com", "/anthropics/claude-code") || isPreapprovedHost("github.com", "/anthropics-evil") {
		t.Fatal("path-scoped preapproval mismatch")
	}
	if !isPreapprovedHost("vercel.com", "/docs") || isPreapprovedHost("www.vercel.com", "/docs") {
		t.Fatal("host preapproval mismatch")
	}
}

func TestMapToolResultToToolResultBlockParam(t *testing.T) {
	block := MapToolResultToToolResultBlockParam(Output{Result: "only this"}, "toolu_1")
	if block.ToolUseID != "toolu_1" || block.Type != "tool_result" || block.Content != "only this" {
		t.Fatalf("block = %#v", block)
	}
}

func TestDurationClampedAtZero(t *testing.T) {
	calls := 0
	tool := New(Config{
		Client:        &fakeMessageCreator{response: modelResponse("ok")},
		SkipPreflight: true,
		HTTPClient:    doerFunc(func(*http.Request) (*http.Response, error) { return response(200, "text/plain", "body"), nil }),
		Now: func() time.Time {
			calls++
			if calls == 1 {
				return time.Unix(2, 0)
			}
			return time.Unix(1, 0)
		},
	})
	got, err := tool.Call(context.Background(), Input{URL: "https://example.com", Prompt: "p"})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got.DurationMs != 0 {
		t.Fatalf("duration = %d", got.DurationMs)
	}
}

func TestDefinitionAndParseInput(t *testing.T) {
	definition := Definition()
	if definition.Name != ToolName || definition.Description != ToolPrompt || strings.Contains(definition.Description, "gh CLI") {
		t.Fatalf("definition = %#v", definition)
	}
	var schema struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.AdditionalProperties || len(schema.Properties) != 2 || !reflect.DeepEqual(schema.Required, []string{"url", "prompt"}) {
		t.Fatalf("schema = %#v", schema)
	}

	input, err := ParseInput([]byte(`{"url":"https://example.com/path","prompt":"Summarize"}`))
	if err != nil || input.URL != "https://example.com/path" || input.Prompt != "Summarize" {
		t.Fatalf("input = %#v, error = %v", input, err)
	}
	for _, value := range []string{
		``, `null`, `[]`, `{}`, `{"URL":"https://example.com","prompt":"p"}`,
		`{"url":"https://example.com"}`, `{"url":null,"prompt":"p"}`,
		`{"url":"not a URL","prompt":"p"}`, `{"url":"https://example.com","prompt":null}`,
		`{"url":"https://example.com","prompt":"p","extra":true}`,
		`{"url":"https://example.com","prompt":"p"} {}`,
	} {
		if _, err := ParseInput([]byte(value)); err == nil {
			t.Fatalf("ParseInput(%q) succeeded", value)
		}
	}
}
