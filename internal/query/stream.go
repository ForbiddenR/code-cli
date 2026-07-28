// Package query assembles streamed API events into complete model responses.
package query

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
)

var (
	// ErrInvalidStream identifies an event that violates the Messages stream protocol.
	ErrInvalidStream = errors.New("invalid message stream")
	// ErrIncompleteStream identifies a stream that ended before message_stop.
	ErrIncompleteStream = errors.New("incomplete message stream")
)

type streamState uint8

const (
	streamAwaitingStart streamState = iota
	streamBetweenBlocks
	streamInBlock
	streamAwaitingStop
	streamStopped
)

// StreamAssembler validates normalized stream events and reconstructs a response.
type StreamAssembler struct {
	state      streamState
	response   *anthropicapi.MessageResponse
	blocks     []*contentAssembly
	openBlocks int
	streamErr  error
	finished   bool
	finishErr  error
}

type contentAssembly struct {
	index   int
	block   core.ContentBlock
	json    bytes.Buffer
	sawJSON bool
	stopped bool
}

// NewStreamAssembler creates an assembler awaiting message_start.
func NewStreamAssembler() *StreamAssembler {
	return &StreamAssembler{state: streamAwaitingStart}
}

// Add validates and applies one normalized stream event.
func (a *StreamAssembler) Add(event anthropicapi.StreamEvent) error {
	if a == nil {
		return invalid("nil assembler")
	}
	if a.finished {
		return invalid("event %q after finish", event.Type)
	}
	if a.streamErr != nil {
		return a.streamErr
	}

	if event.Error != nil {
		a.streamErr = fmt.Errorf("%w: stream error: %w", ErrInvalidStream, event.Error)
		return a.streamErr
	}

	var err error
	switch event.Type {
	case anthropicapi.StreamEventMessageStart:
		err = a.addMessageStart(event)
	case anthropicapi.StreamEventContentBlockStart:
		err = a.addContentBlockStart(event)
	case anthropicapi.StreamEventContentBlockDelta:
		err = a.addContentBlockDelta(event)
	case anthropicapi.StreamEventContentBlockStop:
		err = a.addContentBlockStop(event)
	case anthropicapi.StreamEventMessageDelta:
		err = a.addMessageDelta(event)
	case anthropicapi.StreamEventMessageStop:
		err = a.addMessageStop(event)
	case anthropicapi.StreamEventError:
		if event.Error == nil {
			err = invalid("error event without error")
		} else {
			err = fmt.Errorf("%w: stream error: %w", ErrInvalidStream, event.Error)
		}
	default:
		err = invalid("unknown event type %q", event.Type)
	}
	if err != nil {
		a.streamErr = err
	}
	return err
}

// Finish marks the event source closed and verifies that the stream completed.
func (a *StreamAssembler) Finish() error {
	if a == nil {
		return invalid("nil assembler")
	}
	if a.finished {
		return a.finishErr
	}
	a.finished = true
	if a.streamErr != nil {
		a.finishErr = a.streamErr
	} else if a.state != streamStopped {
		a.finishErr = fmt.Errorf("%w: ended in state %s", ErrIncompleteStream, a.state)
	}
	return a.finishErr
}

// Usage returns the latest observed usage, including for an incomplete stream.
func (a *StreamAssembler) Usage() core.Usage {
	if a == nil || a.response == nil {
		return core.EmptyUsage()
	}
	return a.response.Usage
}

// Response returns a defensive copy of the response after a successful Finish.
func (a *StreamAssembler) Response() (*anthropicapi.MessageResponse, error) {
	if a == nil {
		return nil, invalid("nil assembler")
	}
	if !a.finished {
		return nil, fmt.Errorf("%w: response requested before finish", ErrIncompleteStream)
	}
	if a.finishErr != nil {
		return nil, a.finishErr
	}
	if a.response == nil {
		return nil, fmt.Errorf("%w: missing response", ErrIncompleteStream)
	}
	return cloneResponse(a.response), nil
}

func (a *StreamAssembler) addMessageStart(event anthropicapi.StreamEvent) error {
	if a.state != streamAwaitingStart {
		return invalid("message_start in state %s", a.state)
	}
	if event.Message == nil {
		return invalid("message_start without message")
	}
	if event.Message.Role != core.RoleAssistant {
		return invalid("message_start role %q", event.Message.Role)
	}
	if len(event.Message.Content) != 0 {
		return invalid("message_start contains %d content blocks", len(event.Message.Content))
	}
	if event.Message.StopReason != "" || event.Message.StopSequence != "" {
		return invalid("message_start contains stop metadata")
	}

	a.response = cloneResponse(event.Message)
	a.response.Content = nil
	a.state = streamBetweenBlocks
	return nil
}

func (a *StreamAssembler) addContentBlockStart(event anthropicapi.StreamEvent) error {
	if a.state != streamBetweenBlocks && a.state != streamInBlock {
		return invalid("content_block_start in state %s", a.state)
	}
	if event.Block == nil {
		return invalid("content_block_start without block")
	}
	if event.Index != len(a.blocks) {
		return invalid("content block index %d, want %d", event.Index, len(a.blocks))
	}
	if !knownContentType(event.Block.Type) {
		return invalid("unknown content block type %q", event.Block.Type)
	}

	a.blocks = append(a.blocks, &contentAssembly{
		index: event.Index,
		block: cloneBlock(*event.Block),
	})
	a.openBlocks++
	a.state = streamInBlock
	return nil
}

func (a *StreamAssembler) addContentBlockDelta(event anthropicapi.StreamEvent) error {
	if a.state != streamInBlock {
		return invalid("content_block_delta in state %s", a.state)
	}
	active, err := a.openBlock(event.Index, "delta")
	if err != nil {
		return err
	}
	if event.Delta == nil {
		return invalid("content_block_delta without delta")
	}

	delta := event.Delta
	switch delta.Type {
	case "text_delta":
		if active.block.Type != core.ContentBlockText || delta.PartialJSON != "" || delta.Thinking != "" || delta.Signature != "" {
			return invalid("text_delta for block type %q", active.block.Type)
		}
		active.block.Text += delta.Text
	case "thinking_delta":
		if active.block.Type != core.ContentBlockThinking || delta.Text != "" || delta.PartialJSON != "" || delta.Signature != "" {
			return invalid("thinking_delta for block type %q", active.block.Type)
		}
		active.block.Thinking += delta.Thinking
	case "signature_delta":
		if active.block.Type != core.ContentBlockThinking || delta.Text != "" || delta.PartialJSON != "" || delta.Thinking != "" {
			return invalid("signature_delta for block type %q", active.block.Type)
		}
		active.block.Signature += delta.Signature
	case "input_json_delta":
		if !isInputBlock(active.block.Type) || delta.Text != "" || delta.Thinking != "" || delta.Signature != "" {
			return invalid("input_json_delta for block type %q", active.block.Type)
		}
		active.sawJSON = true
		active.json.WriteString(delta.PartialJSON)
	default:
		return invalid("unknown content delta type %q", delta.Type)
	}
	return nil
}

func (a *StreamAssembler) addContentBlockStop(event anthropicapi.StreamEvent) error {
	if a.state != streamInBlock {
		return invalid("content_block_stop in state %s", a.state)
	}
	active, err := a.openBlock(event.Index, "stop")
	if err != nil {
		return err
	}

	block := active.block
	if isInputBlock(block.Type) {
		input := block.Input
		if active.sawJSON {
			input = append(json.RawMessage(nil), active.json.Bytes()...)
		}
		if err := validateToolInput(input); err != nil {
			return invalid("content block %d input: %v", event.Index, err)
		}
		block.Input = append(json.RawMessage(nil), input...)
	}

	active.block = block
	active.stopped = true
	a.openBlocks--
	if a.openBlocks == 0 {
		a.state = streamBetweenBlocks
	}
	return nil
}

func (a *StreamAssembler) openBlock(index int, event string) (*contentAssembly, error) {
	if index < 0 || index >= len(a.blocks) {
		return nil, invalid("content %s index %d has not started", event, index)
	}
	block := a.blocks[index]
	if block.stopped {
		return nil, invalid("content %s index %d already stopped", event, index)
	}
	return block, nil
}

func (a *StreamAssembler) addMessageDelta(event anthropicapi.StreamEvent) error {
	if a.state != streamBetweenBlocks {
		return invalid("message_delta in state %s", a.state)
	}
	if event.MessageDelta == nil {
		return invalid("message_delta without metadata")
	}
	if !knownStopReason(event.MessageDelta.StopReason) {
		return invalid("unknown stop reason %q", event.MessageDelta.StopReason)
	}
	if event.Usage == nil {
		return invalid("message_delta without usage")
	}

	a.response.Content = make([]core.ContentBlock, len(a.blocks))
	for index, block := range a.blocks {
		if !block.stopped {
			return invalid("content block %d missing stop", index)
		}
		a.response.Content[index] = cloneBlock(block.block)
	}
	a.response.StopReason = event.MessageDelta.StopReason
	a.response.StopSequence = event.MessageDelta.StopSequence
	// Input and cache usage arrive on message_start. The final delta's output
	// count is cumulative and replaces the start value rather than adding to it.
	a.response.Usage.OutputTokens = event.Usage.OutputTokens
	a.state = streamAwaitingStop
	return nil
}

func (a *StreamAssembler) addMessageStop(event anthropicapi.StreamEvent) error {
	if a.state != streamAwaitingStop {
		return invalid("message_stop in state %s", a.state)
	}
	a.state = streamStopped
	return nil
}

func validateToolInput(input json.RawMessage) error {
	if len(input) == 0 {
		return errors.New("empty JSON")
	}
	if !json.Valid(input) {
		return errors.New("malformed JSON")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(input, &object); err != nil || object == nil {
		return errors.New("JSON must be an object")
	}
	return nil
}

func isInputBlock(blockType core.ContentBlockType) bool {
	return blockType == core.ContentBlockToolUse || blockType == core.ContentBlockServerToolUse
}

func knownContentType(blockType core.ContentBlockType) bool {
	switch blockType {
	case core.ContentBlockText,
		core.ContentBlockImage,
		core.ContentBlockDocument,
		core.ContentBlockToolUse,
		core.ContentBlockToolResult,
		core.ContentBlockThinking,
		core.ContentBlockRedactedThinking,
		core.ContentBlockServerToolUse,
		core.ContentBlockWebSearchToolResult,
		core.ContentBlockWebSearchResult,
		core.ContentBlockCodeExecutionTool:
		return true
	default:
		return false
	}
}

func knownStopReason(reason core.StopReason) bool {
	switch reason {
	case core.StopReasonEndTurn,
		core.StopReasonMaxTokens,
		core.StopReasonStopSequence,
		core.StopReasonToolUse,
		core.StopReasonPauseTurn,
		core.StopReasonRefusal:
		return true
	default:
		return false
	}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidStream, fmt.Sprintf(format, args...))
}

func cloneResponse(response *anthropicapi.MessageResponse) *anthropicapi.MessageResponse {
	if response == nil {
		return nil
	}
	clone := *response
	clone.Content = make([]core.ContentBlock, len(response.Content))
	for i := range response.Content {
		clone.Content[i] = cloneBlock(response.Content[i])
	}
	return &clone
}

func cloneBlock(block core.ContentBlock) core.ContentBlock {
	block.Input = append(json.RawMessage(nil), block.Input...)
	if block.Source != nil {
		source := *block.Source
		block.Source = &source
	}
	if block.CacheControl != nil {
		cacheControl := *block.CacheControl
		block.CacheControl = &cacheControl
	}
	if block.Content != nil {
		content := block.Content
		block.Content = make([]core.ContentBlock, len(content))
		for i := range content {
			block.Content[i] = cloneBlock(content[i])
		}
	}
	return block
}

func (s streamState) String() string {
	switch s {
	case streamAwaitingStart:
		return "awaiting message_start"
	case streamBetweenBlocks:
		return "between content blocks"
	case streamInBlock:
		return "inside content block"
	case streamAwaitingStop:
		return "awaiting message_stop"
	case streamStopped:
		return "stopped"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}
