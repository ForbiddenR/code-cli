package query

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
	"code-cli/internal/tools"
)

type fakeModelClient struct {
	mu        sync.Mutex
	responses []*anthropicapi.MessageResponse
	errors    []error
	requests  []anthropicapi.MessageRequest
}

func (client *fakeModelClient) StreamMessage(ctx context.Context, request anthropicapi.MessageRequest, _ ...anthropicapi.CallOption) (anthropicapi.Stream, error) {
	if ctx == nil {
		panic("nil context")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	client.requests = append(client.requests, cloneRequest(request))
	index := len(client.requests) - 1
	if index < len(client.errors) && client.errors[index] != nil {
		return nil, client.errors[index]
	}
	if index >= len(client.responses) {
		return nil, errors.New("unexpected model request")
	}
	return newFakeStream(responseEvents(client.responses[index])), nil
}

type fakeStream struct {
	events chan anthropicapi.StreamEvent
}

func newFakeStream(events []anthropicapi.StreamEvent) *fakeStream {
	stream := &fakeStream{events: make(chan anthropicapi.StreamEvent, len(events))}
	for _, event := range events {
		stream.events <- event
	}
	close(stream.events)
	return stream
}

func (stream *fakeStream) Events() <-chan anthropicapi.StreamEvent { return stream.events }
func (stream *fakeStream) Close() error                            { return nil }

type fakeRuntime struct {
	definitions []core.ToolDefinition
	classify    func(string, json.RawMessage) (tools.InputClassification, error)
	execute     func(context.Context, string, json.RawMessage, tools.ExecuteOptions) (tools.ExecutionResult, error)
	calls       []string
}

func (runtime *fakeRuntime) EnabledDefinitions() []core.ToolDefinition {
	return cloneDefinitions(runtime.definitions)
}

func (runtime *fakeRuntime) Classify(name string, input json.RawMessage) (tools.InputClassification, error) {
	if runtime.classify != nil {
		return runtime.classify(name, input)
	}
	return tools.InputClassification{ReadOnly: true}, nil
}

func (runtime *fakeRuntime) Execute(ctx context.Context, name string, input json.RawMessage, options tools.ExecuteOptions) (tools.ExecutionResult, error) {
	runtime.calls = append(runtime.calls, name)
	if runtime.execute != nil {
		return runtime.execute(ctx, name, input, options)
	}
	return tools.ExecutionResult{
		CanonicalName: name,
		ToolResult:    core.ToolResultBlock(options.ToolUseID, []core.ContentBlock{core.TextBlock("ok")}, false),
	}, nil
}

func TestEngineRunsToolLoopWithCanonicalOrderingAndEffects(t *testing.T) {
	first := response(core.StopReasonEndTurn,
		toolUse("call_1", "alpha", `{"value":1}`),
		core.TextBlock("between"),
		toolUse("call_2", "beta", `{"value":2}`),
	)
	first.StopReason = core.StopReasonEndTurn // Calls are detected from content, not only stop reason.
	first.Usage = core.Usage{InputTokens: 10, OutputTokens: 3}
	second := response(core.StopReasonEndTurn, core.TextBlock("done"))
	second.Usage = core.Usage{InputTokens: 20, OutputTokens: 4, CacheReadInputTokens: 5}
	client := &fakeModelClient{responses: []*anthropicapi.MessageResponse{first, second}}

	high := core.EffortHigh
	runtime := &fakeRuntime{
		definitions: []core.ToolDefinition{
			{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "beta", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}
	runtime.execute = func(_ context.Context, name string, _ json.RawMessage, options tools.ExecuteOptions) (tools.ExecutionResult, error) {
		runtime.calls = append(runtime.calls, "inner:"+name)
		switch name {
		case "alpha":
			return tools.ExecutionResult{
				CanonicalName: "alpha",
				ToolResult:    core.ToolResultBlock(options.ToolUseID, []core.ContentBlock{core.TextBlock("kept despite error")}, false),
				NewMessages: []tools.InjectedMessage{{
					Message:         core.UserMessage("injected one"),
					IsMeta:          true,
					SourceToolUseID: options.ToolUseID,
				}},
				ContextEffects: &tools.ContextEffects{
					AllowedTools:          []string{"beta", "gamma"},
					AllowedToolsSpecified: true,
					Model:                 "sonnet",
					Effort:                &high,
				},
			}, errors.New("host warning")
		case "beta":
			return tools.ExecutionResult{
				CanonicalName: "beta",
				ToolResult:    core.ToolResultBlock(options.ToolUseID, []core.ContentBlock{core.TextBlock("second")}, false),
				NewMessages:   []tools.InjectedMessage{{Message: core.UserMessage("injected two")}},
				ContextEffects: &tools.ContextEffects{
					AllowedTools:          []string{"alpha", "beta"},
					AllowedToolsSpecified: true,
				},
			}, nil
		default:
			panic(name)
		}
	}

	request := anthropicapi.MessageRequest{
		Model:     core.ModelClaudeOpus48,
		MaxTokens: 100,
		Messages:  []core.Message{core.UserMessage("prior")},
		Metadata:  map[string]string{"key": "value"},
	}
	engine, err := NewEngine(Config{Client: client, Runtime: runtime, Authorizer: AllowAll{}, Request: request})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var eventTypes []EventType
	result, err := engine.Run(context.Background(), "question", func(event Event) {
		eventTypes = append(eventTypes, event.Type)
		if event.Message != nil && len(event.Message.Content) != 0 {
			event.Message.Content[0].Text = "mutated callback copy"
		}
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outcome != OutcomeEndTurn || result.ToolTurns != 1 || result.Usage != (core.Usage{InputTokens: 30, OutputTokens: 7, CacheReadInputTokens: 5}) {
		t.Fatalf("result = %#v", result)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d", len(client.requests))
	}
	secondRequest := client.requests[1]
	if secondRequest.Model != core.ModelClaudeSonnet5 || secondRequest.OutputConfig == nil || secondRequest.OutputConfig.Effort != core.EffortHigh {
		t.Fatalf("later model config = %#v, %#v", secondRequest.Model, secondRequest.OutputConfig)
	}
	if got := definitionNames(secondRequest.Tools); !reflect.DeepEqual(got, []string{"beta"}) {
		t.Fatalf("later tools = %#v", got)
	}

	messages := secondRequest.Messages
	if len(messages) != 6 {
		t.Fatalf("later history length = %d: %#v", len(messages), messages)
	}
	wantRoles := []core.Role{core.RoleUser, core.RoleUser, core.RoleAssistant, core.RoleUser, core.RoleUser, core.RoleUser, core.RoleAssistant}
	gotRoles := make([]core.Role, len(result.History))
	for index := range result.History {
		gotRoles[index] = result.History[index].Role
	}
	if !reflect.DeepEqual(gotRoles, wantRoles) {
		t.Fatalf("history roles = %#v, want %#v", gotRoles, wantRoles)
	}
	toolResults := messages[3]
	if len(toolResults.Content) != 2 || toolResults.Content[0].ToolUseID != "call_1" || toolResults.Content[0].Content[0].Text != "kept despite error" || !toolResults.Content[0].IsError {
		t.Fatalf("first preserved tool result = %#v", toolResults.Content)
	}
	if len(toolResults.Content[0].Content) != 2 || !strings.Contains(toolResults.Content[0].Content[1].Text, "host warning") {
		t.Fatalf("first tool error detail = %#v", toolResults.Content[0].Content)
	}
	if toolResults.Content[1].ToolUseID != "call_2" || toolResults.Content[1].Content[0].Text != "second" {
		t.Fatalf("second tool result = %#v", toolResults.Content[1])
	}
	if messages[4].Content[0].Text != "injected one" || messages[5].Content[0].Text != "injected two" {
		t.Fatalf("injected ordering = %#v, %#v", messages[4], messages[5])
	}
	if got := runtime.calls; !reflect.DeepEqual(got, []string{"alpha", "inner:alpha", "beta", "inner:beta"}) {
		t.Fatalf("sequential calls = %#v", got)
	}
	if len(eventTypes) == 0 || eventTypes[len(eventTypes)-1] != EventCompleted {
		t.Fatalf("event types = %#v", eventTypes)
	}
	if result.History[0].Content[0].Text != "prior" {
		t.Fatalf("callback mutated canonical history: %#v", result.History[0])
	}
}

func TestEngineRejectsUnadvertisedRegistryAliasBeforeAuthorization(t *testing.T) {
	client := &fakeModelClient{responses: []*anthropicapi.MessageResponse{
		response(core.StopReasonToolUse, toolUse("call", "Brief", `{}`)),
		response(core.StopReasonEndTurn, core.TextBlock("recovered")),
	}}
	runtime := &fakeRuntime{definitions: []core.ToolDefinition{{Name: "SendUserMessage", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	authorized := false
	engine, err := NewEngine(Config{
		Client:  client,
		Runtime: runtime,
		Authorizer: AuthorizeFunc(func(context.Context, ToolCall) error {
			authorized = true
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, err := engine.Run(context.Background(), "go", nil)
	if err != nil || result.Outcome != OutcomeEndTurn {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if authorized || len(runtime.calls) != 0 {
		t.Fatalf("alias authorized=%v executed=%#v", authorized, runtime.calls)
	}
	block := result.History[2].Content[0]
	if !block.IsError || !strings.Contains(block.Content[0].Text, "not advertised") {
		t.Fatalf("alias result = %#v", block)
	}
}

func TestEngineSynthesizesFailuresForEveryToolCall(t *testing.T) {
	tests := []struct {
		name       string
		runtime    ToolRuntime
		authorizer Authorizer
		wantText   string
	}{
		{name: "no tools", runtime: NoTools{}, authorizer: AllowAll{}, wantText: "not advertised"},
		{name: "malformed", runtime: &fakeRuntime{definitions: []core.ToolDefinition{{Name: "missing", InputSchema: json.RawMessage(`{"type":"object"}`)}}, classify: func(string, json.RawMessage) (tools.InputClassification, error) {
			return tools.InputClassification{}, tools.ErrInvalidInput
		}}, authorizer: AllowAll{}, wantText: "invalid tool input"},
		{name: "denied", runtime: &fakeRuntime{definitions: []core.ToolDefinition{{Name: "missing", InputSchema: json.RawMessage(`{"type":"object"}`)}}}, authorizer: DenyAll{}, wantText: "tool use denied"},
		{name: "mismatched result", runtime: &fakeRuntime{definitions: []core.ToolDefinition{{Name: "missing", InputSchema: json.RawMessage(`{"type":"object"}`)}}, execute: func(_ context.Context, _ string, _ json.RawMessage, _ tools.ExecuteOptions) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{ToolResult: core.ToolResultBlock("wrong", []core.ContentBlock{core.TextBlock("bad")}, false)}, nil
		}}, authorizer: AllowAll{}, wantText: "tool call failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeModelClient{responses: []*anthropicapi.MessageResponse{
				response(core.StopReasonToolUse, toolUse("a", "missing", `{}`), toolUse("b", "missing", `{}`)),
				response(core.StopReasonEndTurn, core.TextBlock("recovered")),
			}}
			engine, err := NewEngine(Config{Client: client, Runtime: test.runtime, Authorizer: test.authorizer})
			if err != nil {
				t.Fatalf("NewEngine() error = %v", err)
			}
			result, err := engine.Run(context.Background(), "go", nil)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			results := result.History[2]
			if len(results.Content) != 2 {
				t.Fatalf("tool results = %#v", results)
			}
			for index, block := range results.Content {
				if !block.IsError || block.ToolUseID != []string{"a", "b"}[index] || !contains(block.Content[0].Text, test.wantText) {
					t.Fatalf("result %d = %#v", index, block)
				}
			}
		})
	}
}

func TestEngineToolTurnLimitReturnsResultsWithoutAnotherRequest(t *testing.T) {
	client := &fakeModelClient{responses: []*anthropicapi.MessageResponse{
		response(core.StopReasonToolUse, toolUse("first", "run", `{}`)),
		response(core.StopReasonToolUse, toolUse("limited-a", "run", `{}`), toolUse("limited-b", "run", `{}`)),
	}}
	runtime := &fakeRuntime{definitions: []core.ToolDefinition{{Name: "run", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	engine, err := NewEngine(Config{Client: client, Runtime: runtime, Authorizer: AllowAll{}, MaxToolTurns: 1})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, err := engine.Run(context.Background(), "go", nil)
	if !errors.Is(err, ErrToolTurnLimit) || result.Outcome != OutcomeToolTurnLimit || len(client.requests) != 2 {
		t.Fatalf("result=%#v err=%v requests=%d", result, err, len(client.requests))
	}
	if !reflect.DeepEqual(runtime.calls, []string{"run"}) {
		t.Fatalf("executed calls = %#v", runtime.calls)
	}
	last := result.History[len(result.History)-1]
	if len(last.Content) != 2 || !last.Content[0].IsError || !last.Content[1].IsError {
		t.Fatalf("limited results = %#v", last)
	}
}

func TestEngineCancellationSynthesizesRemainingResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeModelClient{responses: []*anthropicapi.MessageResponse{
		response(core.StopReasonToolUse, toolUse("one", "run", `{}`), toolUse("two", "run", `{}`)),
	}}
	runtime := &fakeRuntime{definitions: []core.ToolDefinition{{Name: "run", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	runtime.execute = func(ctx context.Context, _ string, _ json.RawMessage, _ tools.ExecuteOptions) (tools.ExecutionResult, error) {
		cancel()
		return tools.ExecutionResult{}, ctx.Err()
	}
	engine, err := NewEngine(Config{Client: client, Runtime: runtime, Authorizer: AllowAll{}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, err := engine.Run(ctx, "go", nil)
	if !errors.Is(err, context.Canceled) || result.Outcome != OutcomeCanceled {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !reflect.DeepEqual(runtime.calls, []string{"run"}) {
		t.Fatalf("executed calls = %#v", runtime.calls)
	}
	last := result.History[len(result.History)-1]
	if len(last.Content) != 2 || last.Content[0].ToolUseID != "one" || last.Content[1].ToolUseID != "two" || !last.Content[0].IsError || !last.Content[1].IsError {
		t.Fatalf("canceled results = %#v", last.Content)
	}
}

func TestEngineCanceledQueuedSubmissionDoesNotChangeHistory(t *testing.T) {
	entered := make(chan struct{})
	streamEvents := make(chan anthropicapi.StreamEvent)
	client := ModelClientFunc(func(context.Context, anthropicapi.MessageRequest, ...anthropicapi.CallOption) (anthropicapi.Stream, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		return &fakeStream{events: streamEvents}, nil
	})
	engine, err := NewEngine(Config{Client: client})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, runErr := engine.Run(firstContext, "first", nil)
		firstDone <- runErr
	}()
	<-entered

	secondContext, cancelSecond := context.WithCancel(context.Background())
	cancelSecond()
	result, err := engine.Run(secondContext, "second", nil)
	if !errors.Is(err, context.Canceled) || result.Outcome != OutcomeCanceled {
		t.Fatalf("queued result=%#v err=%v", result, err)
	}
	if history := engine.History(); len(history) != 1 || history[0].Content[0].Text != "first" {
		t.Fatalf("queued cancellation changed history: %#v", history)
	}
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run() error = %v", err)
	}
}

func TestSubmitEventsSlowConsumerUsesBoundedBackpressureAndCancellation(t *testing.T) {
	client := &fakeModelClient{responses: []*anthropicapi.MessageResponse{
		response(core.StopReasonEndTurn, core.TextBlock("first")),
		response(core.StopReasonEndTurn, core.TextBlock("second")),
	}}
	engine, err := NewEngine(Config{Client: client})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := engine.SubmitEvents(ctx, core.UserMessage("one"), 1)
	cancel()
	for range events {
	}
	result, err := engine.Run(context.Background(), "two", nil)
	if err != nil || result.Outcome != OutcomeEndTurn {
		t.Fatalf("second result=%#v err=%v", result, err)
	}
}

func TestSubmitEventsPreservesOrderForSlowActiveConsumer(t *testing.T) {
	client := &fakeModelClient{responses: []*anthropicapi.MessageResponse{
		response(core.StopReasonEndTurn, core.TextBlock("done")),
	}}
	engine, err := NewEngine(Config{Client: client})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var types []EventType
	for event := range engine.SubmitEvents(context.Background(), core.UserMessage("one"), 1) {
		types = append(types, event.Type)
	}
	want := []EventType{
		EventStream, EventStream, EventStream, EventStream, EventStream,
		EventAssistantMessage, EventCompleted,
	}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("event types = %#v, want %#v", types, want)
	}
}

func TestEngineTreatsToolDeadlineErrorAsOrdinaryWhenParentContextActive(t *testing.T) {
	client := &fakeModelClient{responses: []*anthropicapi.MessageResponse{
		response(core.StopReasonToolUse, toolUse("one", "run", `{}`), toolUse("two", "run", `{}`)),
		response(core.StopReasonEndTurn, core.TextBlock("recovered")),
	}}
	runtime := &fakeRuntime{definitions: []core.ToolDefinition{{Name: "run", InputSchema: json.RawMessage(`{"type":"object"}`)}}, execute: func(context.Context, string, json.RawMessage, tools.ExecuteOptions) (tools.ExecutionResult, error) {
		return tools.ExecutionResult{}, errors.Join(errors.New("tool timeout"), context.DeadlineExceeded)
	}}
	engine, err := NewEngine(Config{Client: client, Runtime: runtime, Authorizer: AllowAll{}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, err := engine.Run(context.Background(), "go", nil)
	if err != nil || result.Outcome != OutcomeEndTurn {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !reflect.DeepEqual(runtime.calls, []string{"run", "run"}) || len(client.requests) != 2 {
		t.Fatalf("executed calls = %#v, requests = %d", runtime.calls, len(client.requests))
	}
	toolResults := result.History[2]
	if len(toolResults.Content) != 2 || !toolResults.Content[0].IsError || !toolResults.Content[1].IsError {
		t.Fatalf("deadline results = %#v", toolResults.Content)
	}
}

func TestEngineRejectsToolUseStopWithoutCalls(t *testing.T) {
	client := &fakeModelClient{responses: []*anthropicapi.MessageResponse{response(core.StopReasonToolUse, core.TextBlock("missing"))}}
	engine, err := NewEngine(Config{Client: client})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, err := engine.Run(context.Background(), "go", nil)
	if !errors.Is(err, ErrToolUseWithoutCalls) || result.Outcome != OutcomeFailed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestEngineDefensiveCopiesAndEventChannel(t *testing.T) {
	request := anthropicapi.MessageRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentBlock{{
			Type:   core.ContentBlockDocument,
			Input:  json.RawMessage(`{"nested":true}`),
			Source: &core.ContentSource{Data: "original"},
		}}}},
		Metadata: map[string]string{"a": "b"},
	}
	client := &fakeModelClient{responses: []*anthropicapi.MessageResponse{response(core.StopReasonEndTurn, core.TextBlock("done"))}}
	engine, err := NewEngine(Config{Client: client, Request: request})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request.Messages[0].Content[0].Source.Data = "changed"
	request.Messages[0].Content[0].Input[0] = 'x'
	request.Metadata["a"] = "changed"

	var completed *Result
	for event := range engine.SubmitEvents(context.Background(), core.UserMessage("question"), 32) {
		if event.Type == EventCompleted {
			completed = event.Result
		}
	}
	if completed == nil || completed.Outcome != OutcomeEndTurn {
		t.Fatalf("completed = %#v", completed)
	}
	if client.requests[0].Messages[0].Content[0].Source.Data != "original" || string(client.requests[0].Messages[0].Content[0].Input) != `{"nested":true}` || client.requests[0].Metadata["a"] != "b" {
		t.Fatalf("request was not copied: %#v", client.requests[0])
	}
	completed.History[0].Content[0].Source.Data = "mutated"
	if engine.History()[0].Content[0].Source.Data != "original" {
		t.Fatal("result history mutated engine history")
	}
}

func TestEngineValidatesClient(t *testing.T) {
	// Do not pass a nil context.Context even when defensive APIs reject it;
	// use context.TODO()/Background() at call sites (SA1012 / Go convention).
	if _, err := NewEngine(Config{}); !errors.Is(err, ErrNilModelClient) {
		t.Fatalf("NewEngine() error = %v", err)
	}
	engine, err := NewEngine(Config{Client: &fakeModelClient{}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	// A non-nil context with no scripted model response fails cleanly and must
	// not mutate history before the first successful append path.
	result, runErr := engine.Run(context.TODO(), "go", nil)
	if runErr == nil {
		t.Fatal("Run() with empty client expected an error")
	}
	if result.Outcome != OutcomeFailed && result.Outcome != OutcomeCanceled {
		t.Fatalf("result outcome = %q", result.Outcome)
	}
	if len(engine.History()) != 1 {
		// User message is appended after context/client checks succeed.
		// Empty fake client fails during the model request, so history has the user turn.
		t.Fatalf("history after failed request = %#v", engine.History())
	}
}

func response(reason core.StopReason, content ...core.ContentBlock) *anthropicapi.MessageResponse {
	return &anthropicapi.MessageResponse{ID: "message", Model: core.ModelClaudeOpus48, Role: core.RoleAssistant, Content: content, StopReason: reason}
}

func toolUse(id, name, input string) core.ContentBlock {
	return core.ContentBlock{Type: core.ContentBlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

func responseEvents(response *anthropicapi.MessageResponse) []anthropicapi.StreamEvent {
	start := *cloneResponse(response)
	start.Content = nil
	start.StopReason = ""
	start.StopSequence = ""
	start.Usage.OutputTokens = 0
	events := []anthropicapi.StreamEvent{{Type: anthropicapi.StreamEventMessageStart, Message: &start}}
	for index := range response.Content {
		block := cloneBlock(response.Content[index])
		events = append(events,
			anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStart, Index: index, Block: &block},
			anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStop, Index: index},
		)
	}
	events = append(events,
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventMessageDelta, MessageDelta: &anthropicapi.MessageDelta{StopReason: response.StopReason, StopSequence: response.StopSequence}, Usage: &core.Usage{OutputTokens: response.Usage.OutputTokens}},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventMessageStop},
	)
	return events
}

func definitionNames(definitions []core.ToolDefinition) []string {
	names := make([]string, len(definitions))
	for index := range definitions {
		names[index] = definitions[index].Name
	}
	return names
}

func contains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
