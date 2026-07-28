package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
	"code-cli/internal/tools"
)

const DefaultMaxToolTurns = 8

var (
	ErrNilContext          = errors.New("query context is nil")
	ErrNilModelClient      = errors.New("query model client is nil")
	ErrToolUseWithoutCalls = errors.New("tool_use stop without tool_use blocks")
	ErrToolTurnLimit       = errors.New("tool turn limit reached")
)

// ModelClient is the narrow streaming API boundary consumed by Engine.
type ModelClient interface {
	StreamMessage(context.Context, anthropicapi.MessageRequest, ...anthropicapi.CallOption) (anthropicapi.Stream, error)
}

// ModelClientFunc adapts a function to ModelClient.
type ModelClientFunc func(context.Context, anthropicapi.MessageRequest, ...anthropicapi.CallOption) (anthropicapi.Stream, error)

func (fn ModelClientFunc) StreamMessage(ctx context.Context, request anthropicapi.MessageRequest, options ...anthropicapi.CallOption) (anthropicapi.Stream, error) {
	if fn == nil {
		return nil, ErrNilModelClient
	}
	return fn(ctx, request, options...)
}

// Outcome explicitly describes why a submitted query stopped.
type Outcome string

const (
	OutcomeEndTurn       Outcome = "end_turn"
	OutcomeMaxTokens     Outcome = "max_tokens"
	OutcomeStopSequence  Outcome = "stop_sequence"
	OutcomePauseTurn     Outcome = "pause_turn"
	OutcomeRefusal       Outcome = "refusal"
	OutcomeCanceled      Outcome = "canceled"
	OutcomeToolTurnLimit Outcome = "tool_turn_limit"
	OutcomeFailed        Outcome = "failed"
)

// EventType identifies synchronous Engine progress events.
type EventType string

const (
	EventStream           EventType = "stream"
	EventAssistantMessage EventType = "assistant_message"
	EventToolResults      EventType = "tool_results"
	EventInjectedMessage  EventType = "injected_message"
	EventCompleted        EventType = "completed"
)

// Event is a progress value suitable for callbacks or channels. Conversation
// content is defensively copied; ExecutionResult.Output remains opaque host data.
type Event struct {
	Type        EventType
	Stream      *anthropicapi.StreamEvent
	Message     *core.Message
	ToolResults []tools.ExecutionResult
	Result      *Result
	Err         error
}

// EventCallback receives events synchronously on the Submit caller's goroutine.
type EventCallback func(Event)

// Result is the explicit terminal state of one submission.
type Result struct {
	Outcome    Outcome
	StopReason core.StopReason
	Response   *anthropicapi.MessageResponse
	Usage      core.Usage
	History    []core.Message
	ToolTurns  int
}

// Config constructs an Engine around immutable request defaults.
type Config struct {
	Client       ModelClient
	Runtime      ToolRuntime
	Authorizer   Authorizer
	Request      anthropicapi.MessageRequest
	MaxToolTurns int
	SessionID    string
	Progress     tools.ProgressFunc
}

// Engine owns canonical conversation history and runs one submission at a time.
type Engine struct {
	client       ModelClient
	runtime      ToolRuntime
	authorizer   Authorizer
	request      anthropicapi.MessageRequest
	maxToolTurns int
	sessionID    string
	progress     tools.ProgressFunc

	runGate chan struct{}
	mu      sync.RWMutex
	history []core.Message
	allowed map[string]struct{}
	model   core.ModelID
	effort  *core.Effort
}

// NewEngine validates config and takes deep defensive copies of request state.
func NewEngine(config Config) (*Engine, error) {
	if config.Client == nil {
		return nil, ErrNilModelClient
	}
	runtime := config.Runtime
	if runtime == nil {
		runtime = NoTools{}
	}
	authorizer := config.Authorizer
	if authorizer == nil {
		authorizer = DenyAll{}
	}
	maxToolTurns := config.MaxToolTurns
	if maxToolTurns == 0 {
		maxToolTurns = DefaultMaxToolTurns
	}
	if maxToolTurns < 0 {
		return nil, errors.New("maximum tool turns must not be negative")
	}

	request := cloneRequest(config.Request.WithDefaults())
	engine := &Engine{
		client:       config.Client,
		runtime:      runtime,
		authorizer:   authorizer,
		request:      request,
		maxToolTurns: maxToolTurns,
		sessionID:    config.SessionID,
		progress:     config.Progress,
		runGate:      make(chan struct{}, 1),
		history:      cloneMessages(request.Messages),
		model:        request.Model,
	}
	if request.OutputConfig != nil && request.OutputConfig.Effort != "" {
		effort := request.OutputConfig.Effort
		engine.effort = &effort
	}
	return engine, nil
}

// History returns a deep defensive copy of canonical conversation history.
func (engine *Engine) History() []core.Message {
	if engine == nil {
		return nil
	}
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	return cloneMessages(engine.history)
}

// Submit appends one message and synchronously runs the streamed model/tool loop.
func (engine *Engine) Submit(ctx context.Context, message core.Message, callback EventCallback) (Result, error) {
	if ctx == nil {
		result := Result{Outcome: OutcomeFailed}
		emit(callback, Event{Type: EventCompleted, Result: &result, Err: ErrNilContext})
		return result, ErrNilContext
	}
	if engine == nil || engine.client == nil {
		result := Result{Outcome: OutcomeFailed}
		emit(callback, Event{Type: EventCompleted, Result: &result, Err: ErrNilModelClient})
		return result, ErrNilModelClient
	}

	select {
	case engine.runGate <- struct{}{}:
		defer func() { <-engine.runGate }()
	case <-ctx.Done():
		result := Result{Outcome: OutcomeCanceled, History: engine.History()}
		emit(callback, Event{Type: EventCompleted, Result: &result, Err: ctx.Err()})
		return result, ctx.Err()
	}
	engine.appendHistory(message)
	return engine.run(ctx, callback)
}

// Run submits a single user text message.
func (engine *Engine) Run(ctx context.Context, text string, callback EventCallback) (Result, error) {
	return engine.Submit(ctx, core.UserMessage(text), callback)
}

// SubmitEvents runs Submit asynchronously and closes the returned channel after
// completion. Delivery is bounded: slow consumers apply backpressure until they
// receive events or cancel the submission context.
func (engine *Engine) SubmitEvents(ctx context.Context, message core.Message, buffer int) <-chan Event {
	if buffer <= 0 {
		buffer = 1
	}
	events := make(chan Event, buffer)
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	go func() {
		defer close(events)
		completed := false
		send := func(event Event) bool {
			event = cloneEvent(event)
			select {
			case events <- event:
				return true
			case <-done:
				// Preserve a terminal event when an active consumer has capacity,
				// but never wait indefinitely after cancellation.
				if event.Type == EventCompleted {
					select {
					case events <- event:
						return true
					default:
					}
				}
				return false
			}
		}
		result, err := engine.Submit(ctx, message, func(event Event) {
			if send(event) && event.Type == EventCompleted {
				completed = true
			}
		})
		if !completed {
			send(Event{Type: EventCompleted, Result: &result, Err: err})
		}
	}()
	return events
}

func (engine *Engine) run(ctx context.Context, callback EventCallback) (Result, error) {
	var usage core.Usage
	var last *anthropicapi.MessageResponse
	toolTurns := 0

	for {
		if err := ctx.Err(); err != nil {
			result := engine.result(OutcomeCanceled, "", last, usage, toolTurns)
			emit(callback, Event{Type: EventCompleted, Result: &result, Err: err})
			return result, err
		}

		request := engine.nextRequest()
		response, observedUsage, err := engine.stream(ctx, request, callback)
		if err != nil {
			usage = usage.Add(observedUsage)
			outcome := OutcomeFailed
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				outcome = OutcomeCanceled
			}
			result := engine.result(outcome, "", last, usage, toolTurns)
			emit(callback, Event{Type: EventCompleted, Result: &result, Err: err})
			return result, err
		}
		last = response
		usage = usage.Add(response.Usage)

		assistant := core.AssistantMessage(response.Content)
		engine.appendHistory(assistant)
		emit(callback, Event{Type: EventAssistantMessage, Message: messagePointer(assistant)})

		calls := toolUseBlocks(response.Content)
		if response.StopReason == core.StopReasonToolUse && len(calls) == 0 {
			result := engine.result(OutcomeFailed, response.StopReason, response, usage, toolTurns)
			emit(callback, Event{Type: EventCompleted, Result: &result, Err: ErrToolUseWithoutCalls})
			return result, ErrToolUseWithoutCalls
		}
		if len(calls) == 0 {
			outcome := outcomeForStop(response.StopReason)
			result := engine.result(outcome, response.StopReason, response, usage, toolTurns)
			emit(callback, Event{Type: EventCompleted, Result: &result})
			return result, nil
		}

		limitReached := toolTurns >= engine.maxToolTurns
		if !limitReached {
			toolTurns++
		}
		executions, injected, effects, cancelErr := engine.executeRound(ctx, calls, request.Tools, limitReached)
		resultBlocks := make([]core.ContentBlock, len(executions))
		for index := range executions {
			resultBlocks[index] = cloneBlock(executions[index].ToolResult)
		}
		resultMessage := core.Message{Role: core.RoleUser, Content: resultBlocks}
		engine.appendHistory(resultMessage)
		emit(callback, Event{Type: EventToolResults, Message: messagePointer(resultMessage), ToolResults: cloneExecutionResults(executions)})
		for _, message := range injected {
			engine.appendHistory(message.Message)
			emit(callback, Event{Type: EventInjectedMessage, Message: messagePointer(message.Message)})
		}
		engine.applyEffects(effects)

		if limitReached {
			result := engine.result(OutcomeToolTurnLimit, response.StopReason, response, usage, toolTurns)
			emit(callback, Event{Type: EventCompleted, Result: &result, Err: ErrToolTurnLimit})
			return result, ErrToolTurnLimit
		}
		if cancelErr != nil {
			result := engine.result(OutcomeCanceled, response.StopReason, response, usage, toolTurns)
			emit(callback, Event{Type: EventCompleted, Result: &result, Err: cancelErr})
			return result, cancelErr
		}
	}
}

func (engine *Engine) stream(ctx context.Context, request anthropicapi.MessageRequest, callback EventCallback) (*anthropicapi.MessageResponse, core.Usage, error) {
	stream, err := engine.client.StreamMessage(ctx, request)
	if err != nil {
		return nil, core.EmptyUsage(), err
	}
	if stream == nil {
		return nil, core.EmptyUsage(), errors.New("model client returned a nil stream")
	}
	defer stream.Close()

	assembler := NewStreamAssembler()
	for {
		select {
		case <-ctx.Done():
			return nil, assembler.Usage(), ctx.Err()
		case event, ok := <-stream.Events():
			if !ok {
				if err := assembler.Finish(); err != nil {
					return nil, assembler.Usage(), err
				}
				response, err := assembler.Response()
				return response, assembler.Usage(), err
			}
			if err := assembler.Add(event); err != nil {
				return nil, assembler.Usage(), err
			}
			copy := cloneStreamEvent(event)
			emit(callback, Event{Type: EventStream, Stream: &copy})
		}
	}
}

func (engine *Engine) executeRound(ctx context.Context, calls []core.ContentBlock, advertised []core.ToolDefinition, limit bool) ([]tools.ExecutionResult, []tools.InjectedMessage, []*tools.ContextEffects, error) {
	executions := make([]tools.ExecutionResult, 0, len(calls))
	var injected []tools.InjectedMessage
	var effects []*tools.ContextEffects
	var cancelErr error
	advertisedNames := make(map[string]struct{}, len(advertised))
	for _, definition := range advertised {
		advertisedNames[definition.Name] = struct{}{}
	}

	for _, call := range calls {
		var result tools.ExecutionResult
		var err error
		switch {
		case limit:
			err = ErrToolTurnLimit
		case cancelErr != nil:
			err = cancelErr
		case ctx.Err() != nil:
			cancelErr = ctx.Err()
			err = cancelErr
		case !engine.toolAllowed(call.Name):
			err = fmt.Errorf("tool %q is not allowed in the current context", call.Name)
		case !toolAdvertised(advertisedNames, call.Name):
			err = fmt.Errorf("tool %q was not advertised in the current request", call.Name)
		default:
			classification, classifyErr := engine.runtime.Classify(call.Name, call.Input)
			if classifyErr != nil {
				err = classifyErr
				break
			}
			authorizationCall := ToolCall{ID: call.ID, Name: call.Name, Input: append(json.RawMessage(nil), call.Input...), Classification: classification}
			if authorizeErr := engine.authorizer.Authorize(ctx, authorizationCall); authorizeErr != nil {
				err = authorizeErr
				break
			}
			result, err = engine.runtime.Execute(ctx, call.Name, call.Input, tools.ExecuteOptions{ToolUseID: call.ID, SessionID: engine.sessionID, Progress: engine.progress})
		}
		if cancelErr == nil && ctx.Err() != nil {
			cancelErr = ctx.Err()
		}
		if result.ToolResult.Type != core.ContentBlockToolResult || result.ToolResult.ToolUseID != call.ID {
			result.ToolResult = syntheticToolResult(call.ID, err)
		} else if err != nil {
			result.ToolResult.IsError = true
			result.ToolResult.Content = append(result.ToolResult.Content, core.TextBlock("Error: "+err.Error()))
		}
		executions = append(executions, cloneExecutionResult(result))
		injected = append(injected, cloneInjectedMessages(result.NewMessages)...)
		if result.ContextEffects != nil {
			effects = append(effects, cloneContextEffects(result.ContextEffects))
		}
	}
	return executions, injected, effects, cancelErr
}

func syntheticToolResult(toolUseID string, err error) core.ContentBlock {
	message := "tool call failed"
	if err != nil {
		message = err.Error()
	}
	return core.ToolResultBlock(toolUseID, []core.ContentBlock{core.TextBlock("Error: " + message)}, true)
}

func (engine *Engine) nextRequest() anthropicapi.MessageRequest {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	request := cloneRequest(engine.request)
	request.Messages = cloneMessages(engine.history)
	request.Model = engine.model
	definitions := engine.runtime.EnabledDefinitions()
	if engine.allowed != nil {
		filtered := make([]core.ToolDefinition, 0, len(definitions))
		for _, definition := range definitions {
			if _, ok := engine.allowed[definition.Name]; ok {
				filtered = append(filtered, definition)
			}
		}
		definitions = filtered
	}
	request.Tools = cloneDefinitions(definitions)
	if request.ToolChoice != nil && request.ToolChoice.Name != "" {
		available := false
		for _, definition := range definitions {
			if definition.Name == request.ToolChoice.Name {
				available = true
				break
			}
		}
		if !available {
			request.ToolChoice = nil
		}
	}
	if engine.effort == nil {
		request.OutputConfig = nil
	} else {
		request.OutputConfig = &core.OutputConfig{Effort: *engine.effort}
	}
	return request
}

func toolAdvertised(advertised map[string]struct{}, name string) bool {
	_, ok := advertised[name]
	return ok
}

func (engine *Engine) toolAllowed(name string) bool {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.allowed == nil {
		return true
	}
	_, ok := engine.allowed[name]
	return ok
}

func (engine *Engine) applyEffects(all []*tools.ContextEffects) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	for _, effects := range all {
		if effects == nil {
			continue
		}
		if effects.AllowedToolsSpecified {
			next := make(map[string]struct{})
			for _, name := range effects.AllowedTools {
				name = allowedToolName(name)
				if name == "" {
					continue
				}
				if engine.allowed != nil {
					if _, ok := engine.allowed[name]; !ok {
						continue
					}
				}
				next[name] = struct{}{}
			}
			engine.allowed = next
		}
		if model, ok := resolveEffectModel(effects.Model, engine.model); ok {
			engine.model = model
		}
		if effects.Effort != nil {
			effort := *effects.Effort
			engine.effort = &effort
		}
	}
}

func allowedToolName(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '('); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	return value
}

func resolveEffectModel(value string, current core.ModelID) (core.ModelID, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", false
	case "inherit":
		return current, true
	case "fable":
		return core.ModelClaudeFable5, true
	case "opus":
		return core.ModelClaudeOpus48, true
	case "sonnet":
		return core.ModelClaudeSonnet5, true
	case "haiku":
		return core.ModelClaudeHaiku45, true
	default:
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "claude-") {
			return core.ModelID(value), true
		}
		return "", false
	}
}

func (engine *Engine) appendHistory(message core.Message) {
	engine.mu.Lock()
	engine.history = append(engine.history, cloneMessage(message))
	engine.mu.Unlock()
}

func (engine *Engine) result(outcome Outcome, reason core.StopReason, response *anthropicapi.MessageResponse, usage core.Usage, turns int) Result {
	return Result{Outcome: outcome, StopReason: reason, Response: cloneResponse(response), Usage: usage, History: engine.History(), ToolTurns: turns}
}

func outcomeForStop(reason core.StopReason) Outcome {
	switch reason {
	case core.StopReasonEndTurn:
		return OutcomeEndTurn
	case core.StopReasonMaxTokens:
		return OutcomeMaxTokens
	case core.StopReasonStopSequence:
		return OutcomeStopSequence
	case core.StopReasonPauseTurn:
		return OutcomePauseTurn
	case core.StopReasonRefusal:
		return OutcomeRefusal
	default:
		return OutcomeFailed
	}
}

func toolUseBlocks(content []core.ContentBlock) []core.ContentBlock {
	var calls []core.ContentBlock
	for _, block := range content {
		if block.Type == core.ContentBlockToolUse {
			calls = append(calls, cloneBlock(block))
		}
	}
	return calls
}

func emit(callback EventCallback, event Event) {
	if callback != nil {
		callback(cloneEvent(event))
	}
}

func cloneEvent(event Event) Event {
	if event.Stream != nil {
		stream := cloneStreamEvent(*event.Stream)
		event.Stream = &stream
	}
	if event.Message != nil {
		message := cloneMessage(*event.Message)
		event.Message = &message
	}
	event.ToolResults = cloneExecutionResults(event.ToolResults)
	if event.Result != nil {
		result := cloneResult(*event.Result)
		event.Result = &result
	}
	return event
}

func cloneResult(result Result) Result {
	result.Response = cloneResponse(result.Response)
	result.History = cloneMessages(result.History)
	return result
}

func messagePointer(message core.Message) *core.Message {
	copy := cloneMessage(message)
	return &copy
}

func cloneRequest(request anthropicapi.MessageRequest) anthropicapi.MessageRequest {
	request.System = append([]core.SystemBlock(nil), request.System...)
	for index := range request.System {
		if request.System[index].CacheControl != nil {
			cache := *request.System[index].CacheControl
			request.System[index].CacheControl = &cache
		}
	}
	request.Messages = cloneMessages(request.Messages)
	request.Tools = cloneDefinitions(request.Tools)
	request.ServerTools = append([]anthropicapi.ServerToolDefinition(nil), request.ServerTools...)
	for index := range request.ServerTools {
		request.ServerTools[index].AllowedDomains = append([]string(nil), request.ServerTools[index].AllowedDomains...)
		request.ServerTools[index].BlockedDomains = append([]string(nil), request.ServerTools[index].BlockedDomains...)
	}
	if request.ToolChoice != nil {
		choice := *request.ToolChoice
		request.ToolChoice = &choice
	}
	if request.Thinking != nil {
		thinking := *request.Thinking
		request.Thinking = &thinking
	}
	if request.OutputConfig != nil {
		output := *request.OutputConfig
		request.OutputConfig = &output
	}
	request.StopSequences = append([]string(nil), request.StopSequences...)
	request.Betas = append([]string(nil), request.Betas...)
	if request.Metadata != nil {
		metadata := request.Metadata
		request.Metadata = make(map[string]string, len(metadata))
		maps.Copy(request.Metadata, metadata)
	}
	return request
}

func cloneMessages(messages []core.Message) []core.Message {
	result := make([]core.Message, len(messages))
	for index := range messages {
		result[index] = cloneMessage(messages[index])
	}
	return result
}

func cloneMessage(message core.Message) core.Message {
	content := message.Content
	message.Content = make([]core.ContentBlock, len(content))
	for index := range content {
		message.Content[index] = cloneBlock(content[index])
	}
	return message
}

func cloneDefinitions(definitions []core.ToolDefinition) []core.ToolDefinition {
	result := make([]core.ToolDefinition, len(definitions))
	for index := range definitions {
		result[index] = definitions[index]
		result[index].InputSchema = append(json.RawMessage(nil), definitions[index].InputSchema...)
	}
	return result
}

func cloneStreamEvent(event anthropicapi.StreamEvent) anthropicapi.StreamEvent {
	event.Message = cloneResponse(event.Message)
	if event.Block != nil {
		block := cloneBlock(*event.Block)
		event.Block = &block
	}
	if event.Delta != nil {
		delta := *event.Delta
		event.Delta = &delta
	}
	if event.MessageDelta != nil {
		delta := *event.MessageDelta
		event.MessageDelta = &delta
	}
	if event.Usage != nil {
		usage := *event.Usage
		event.Usage = &usage
	}
	return event
}

func cloneExecutionResults(results []tools.ExecutionResult) []tools.ExecutionResult {
	clones := make([]tools.ExecutionResult, len(results))
	for index := range results {
		clones[index] = cloneExecutionResult(results[index])
	}
	return clones
}

func cloneExecutionResult(result tools.ExecutionResult) tools.ExecutionResult {
	result.ToolResult = cloneBlock(result.ToolResult)
	result.NewMessages = cloneInjectedMessages(result.NewMessages)
	result.ContextEffects = cloneContextEffects(result.ContextEffects)
	return result
}

func cloneInjectedMessages(messages []tools.InjectedMessage) []tools.InjectedMessage {
	result := make([]tools.InjectedMessage, len(messages))
	for index := range messages {
		result[index] = messages[index]
		result[index].Message = cloneMessage(messages[index].Message)
	}
	return result
}

func cloneContextEffects(effects *tools.ContextEffects) *tools.ContextEffects {
	if effects == nil {
		return nil
	}
	clone := *effects
	clone.AllowedTools = append([]string(nil), effects.AllowedTools...)
	if effects.Effort != nil {
		effort := *effects.Effort
		clone.Effort = &effort
	}
	return &clone
}
