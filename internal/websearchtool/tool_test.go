package websearchtool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
)

type fakeClient struct {
	request   anthropicapi.MessageRequest
	options   []anthropicapi.CallOption
	stream    anthropicapi.Stream
	streamErr error
}

func (c *fakeClient) CreateMessage(context.Context, anthropicapi.MessageRequest, ...anthropicapi.CallOption) (*anthropicapi.MessageResponse, error) {
	return nil, errors.New("CreateMessage is not used by WebSearchTool tests")
}

func (c *fakeClient) StreamMessage(_ context.Context, request anthropicapi.MessageRequest, options ...anthropicapi.CallOption) (anthropicapi.Stream, error) {
	c.request = request
	c.options = options
	return c.stream, c.streamErr
}

func (c *fakeClient) CountTokens(context.Context, anthropicapi.TokenCountRequest, ...anthropicapi.CallOption) (*anthropicapi.TokenCountResponse, error) {
	return nil, errors.New("CountTokens is not used by WebSearchTool tests")
}

type fakeStream struct {
	events      chan anthropicapi.StreamEvent
	closeCalled bool
}

func newFakeStream(events ...anthropicapi.StreamEvent) *fakeStream {
	channel := make(chan anthropicapi.StreamEvent, len(events))
	for _, event := range events {
		channel <- event
	}
	close(channel)
	return &fakeStream{events: channel}
}

func (s *fakeStream) Events() <-chan anthropicapi.StreamEvent {
	return s.events
}

func (s *fakeStream) Close() error {
	s.closeCalled = true
	return nil
}

func TestValidateInput(t *testing.T) {
	tests := []struct {
		name      string
		input     Input
		valid     bool
		message   string
		errorCode int
	}{
		{name: "missing query", input: Input{}, message: "Error: Missing query", errorCode: 1},
		{name: "short query", input: Input{Query: "x"}, message: "Error: Query must be at least 2 characters", errorCode: 1},
		{
			name:      "conflicting domains",
			input:     Input{Query: "go", AllowedDomains: []string{"go.dev"}, BlockedDomains: []string{"example.com"}},
			message:   "Error: Cannot specify both allowed_domains and blocked_domains in the same request",
			errorCode: 2,
		},
		{name: "valid unicode query", input: Input{Query: "é"}, valid: false, message: "Error: Query must be at least 2 characters", errorCode: 1},
		{name: "valid query", input: Input{Query: "go"}, valid: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ValidateInput(test.input)
			if got.Valid != test.valid || got.Message != test.message || got.ErrorCode != test.errorCode {
				t.Fatalf("ValidateInput() = %#v, want valid=%t message=%q errorCode=%d", got, test.valid, test.message, test.errorCode)
			}
		})
	}
}

func TestIsEnabled(t *testing.T) {
	tests := []struct {
		provider string
		model    core.ModelID
		want     bool
	}{
		{provider: "firstParty", model: core.ModelClaudeHaiku45, want: true},
		{provider: "foundry", model: core.ModelClaudeOpus48, want: true},
		{provider: "vertex", model: "claude-sonnet-4-6", want: true},
		{provider: "vertex", model: "claude-3-7-sonnet", want: false},
		{provider: "bedrock", model: core.ModelClaudeOpus48, want: false},
	}

	for _, test := range tests {
		if got := IsEnabled(test.provider, test.model); got != test.want {
			t.Errorf("IsEnabled(%q, %q) = %t, want %t", test.provider, test.model, got, test.want)
		}
	}
}

func TestPromptUsesSuppliedLocalMonthAndYear(t *testing.T) {
	now := time.Date(2026, time.March, 12, 9, 30, 0, 0, time.Local)
	prompt := Prompt(now)
	for _, want := range []string{"Sources:", "[Title](URL)", "March 2026", "current year"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("Prompt() does not contain %q", want)
		}
	}
}

func TestCallStreamsSearchAndProgress(t *testing.T) {
	start := time.Date(2026, time.March, 12, 9, 30, 0, 0, time.UTC)
	end := start.Add(2250 * time.Millisecond)
	nowCalls := 0

	client := &fakeClient{stream: newFakeStream(
		anthropicapi.StreamEvent{
			Type:  anthropicapi.StreamEventContentBlockStart,
			Index: 0,
			Block: &core.ContentBlock{Type: core.ContentBlockText, Text: "Before "},
		},
		anthropicapi.StreamEvent{
			Type:  anthropicapi.StreamEventContentBlockDelta,
			Index: 0,
			Delta: &anthropicapi.ContentDelta{Type: "text_delta", Text: "search."},
		},
		anthropicapi.StreamEvent{
			Type:  anthropicapi.StreamEventContentBlockStart,
			Index: 1,
			Block: &core.ContentBlock{Type: core.ContentBlockServerToolUse, ID: "srv_1", Name: ServerToolName},
		},
		anthropicapi.StreamEvent{
			Type:  anthropicapi.StreamEventContentBlockDelta,
			Index: 1,
			Delta: &anthropicapi.ContentDelta{Type: "input_json_delta", PartialJSON: `{"query":"weather`},
		},
		anthropicapi.StreamEvent{
			Type:  anthropicapi.StreamEventContentBlockDelta,
			Index: 1,
			Delta: &anthropicapi.ContentDelta{Type: "input_json_delta", PartialJSON: ` today"}`},
		},
		anthropicapi.StreamEvent{
			Type:  anthropicapi.StreamEventContentBlockStart,
			Index: 2,
			Block: &core.ContentBlock{
				Type:      core.ContentBlockWebSearchToolResult,
				ToolUseID: "srv_1",
				Content: []core.ContentBlock{
					{Type: core.ContentBlockWebSearchResult, Title: "Weather", URL: "https://weather.example"},
					{Type: core.ContentBlockWebSearchResult, Title: "Forecast", URL: "https://forecast.example"},
				},
			},
		},
		anthropicapi.StreamEvent{
			Type:  anthropicapi.StreamEventContentBlockStart,
			Index: 3,
			Block: &core.ContentBlock{Type: core.ContentBlockText, Text: "Done."},
		},
	)}
	tool := New(Config{
		Client:    client,
		MainModel: core.ModelClaudeOpus48,
		Thinking:  new(core.DefaultThinking()),
		Now: func() time.Time {
			nowCalls++
			if nowCalls == 1 {
				return start
			}
			return end
		},
	})

	var progress []ProgressEvent
	got, err := tool.Call(context.Background(), Input{Query: "weather"}, func(event ProgressEvent) {
		progress = append(progress, event)
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !client.stream.(*fakeStream).closeCalled {
		t.Fatal("Call() did not close the stream")
	}
	if got.Query != "weather" || got.DurationSeconds != 2.25 {
		t.Fatalf("output identity = %#v", got)
	}
	if len(got.Results) != 3 {
		t.Fatalf("result count = %d, want 3", len(got.Results))
	}
	if got.Results[0].Text != "Before search." {
		t.Fatalf("leading text = %#v", got.Results[0])
	}
	if got.Results[1].Search == nil || got.Results[1].Search.ToolUseID != "srv_1" || len(got.Results[1].Search.Content) != 2 {
		t.Fatalf("search result = %#v", got.Results[1])
	}
	if got.Results[2].Text != "Done." {
		t.Fatalf("trailing text = %#v", got.Results[2])
	}
	if len(progress) != 2 {
		t.Fatalf("progress count = %d, want 2", len(progress))
	}
	if progress[0].Type != ProgressQueryUpdate || progress[0].Query != "weather today" || progress[0].ToolUseID != "search-progress-1" {
		t.Fatalf("query progress = %#v", progress[0])
	}
	if progress[1].Type != ProgressResultsReceived || progress[1].Query != "weather today" || progress[1].ToolUseID != "srv_1" || progress[1].ResultCount != 2 {
		t.Fatalf("result progress = %#v", progress[1])
	}
	if client.request.Model != core.ModelClaudeOpus48 || client.request.Thinking == nil {
		t.Fatalf("main request = %#v", client.request)
	}
	if len(client.request.Messages) != 1 || client.request.Messages[0].Content[0].Text != "Perform a web search for the query: weather" {
		t.Fatalf("request messages = %#v", client.request.Messages)
	}
	if len(client.request.ServerTools) != 1 {
		t.Fatalf("server tools = %#v", client.request.ServerTools)
	}
	serverTool := client.request.ServerTools[0]
	if serverTool.Type != ServerToolType || serverTool.Name != ServerToolName || serverTool.MaxUses != MaxUses {
		t.Fatalf("server tool = %#v", serverTool)
	}
}

func TestCallFastModelForcesWebSearchTool(t *testing.T) {
	client := &fakeClient{stream: newFakeStream()}
	tool := New(Config{
		Client:       client,
		MainModel:    core.ModelClaudeOpus48,
		FastModel:    core.ModelClaudeHaiku45,
		UseFastModel: true,
		Thinking:     new(core.DefaultThinking()),
		Now:          func() time.Time { return time.Unix(100, 0) },
	})

	if _, err := tool.Call(context.Background(), Input{Query: "news"}, nil); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if client.request.Model != core.ModelClaudeHaiku45 {
		t.Fatalf("model = %q, want fast model", client.request.Model)
	}
	if client.request.Thinking != nil {
		t.Fatal("fast request should not include thinking")
	}
	if client.request.ToolChoice == nil || client.request.ToolChoice.Type != "tool" || client.request.ToolChoice.Name != ServerToolName {
		t.Fatalf("tool choice = %#v", client.request.ToolChoice)
	}
}

func TestCallReturnsStreamError(t *testing.T) {
	wantErr := errors.New("stream failed")
	client := &fakeClient{stream: newFakeStream(anthropicapi.StreamEvent{Error: wantErr})}
	tool := New(Config{Client: client, Now: time.Now})

	_, err := tool.Call(context.Background(), Input{Query: "error"}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Call() error = %v, want %v", err, wantErr)
	}
}

func TestMakeOutputFromSearchResponseHandlesErrors(t *testing.T) {
	got := makeOutputFromSearchResponse([]core.ContentBlock{
		{Type: core.ContentBlockServerToolUse, ID: "srv_1"},
		{Type: core.ContentBlockWebSearchToolResult, ToolUseID: "srv_1", ErrorCode: "invalid_query"},
	}, "query", 1)
	if len(got.Results) != 1 || got.Results[0].Text != "Web search error: invalid_query" {
		t.Fatalf("output = %#v", got)
	}
}

func TestMapToolResultToToolResultBlockParam(t *testing.T) {
	block := MapToolResultToToolResultBlockParam(Output{
		Query: "latest Go",
		Results: []Result{
			{Text: "Go 1.26 is current."},
			{Search: &SearchResult{Content: []SearchHit{{Title: "Go", URL: "https://go.dev"}}}},
			{Search: &SearchResult{}},
		},
	}, "toolu_1")

	if block.ToolUseID != "toolu_1" || block.Type != "tool_result" {
		t.Fatalf("block identity = %#v", block)
	}
	for _, want := range []string{
		`Web search results for query: "latest Go"`,
		"Go 1.26 is current.",
		`Links: [{"title":"Go","url":"https://go.dev"}]`,
		"No links found.",
		"REMINDER: You MUST include the sources above in your response to the user using markdown hyperlinks.",
	} {
		if !strings.Contains(block.Content, want) {
			t.Errorf("formatted result does not contain %q: %s", want, block.Content)
		}
	}
}

func TestExtractQueryHandlesEscapedCharacters(t *testing.T) {
	got, ok := extractQuery(`{"query":"cats \"and\" dogs"}`)
	if !ok || got != `cats "and" dogs` {
		t.Fatalf("extractQuery() = %q, %t", got, ok)
	}
}
