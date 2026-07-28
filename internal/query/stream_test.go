package query

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
)

func TestStreamAssemblerReconstructsOrderedContent(t *testing.T) {
	assembler := NewStreamAssembler()
	start := &anthropicapi.MessageResponse{
		ID:    "msg_123",
		Model: core.ModelClaudeOpus48,
		Role:  core.RoleAssistant,
		Usage: core.Usage{
			InputTokens:              11,
			OutputTokens:             0,
			CacheCreationInputTokens: 12,
			CacheReadInputTokens:     13,
		},
	}

	events := []anthropicapi.StreamEvent{
		{Type: anthropicapi.StreamEventMessageStart, Message: start},
		{Type: anthropicapi.StreamEventContentBlockStart, Index: 0, Block: &core.ContentBlock{Type: core.ContentBlockThinking}},
		{Type: anthropicapi.StreamEventContentBlockDelta, Index: 0, Delta: &anthropicapi.ContentDelta{Type: "thinking_delta", Thinking: "look "}},
		{Type: anthropicapi.StreamEventContentBlockDelta, Index: 0, Delta: &anthropicapi.ContentDelta{Type: "thinking_delta", Thinking: "closely"}},
		{Type: anthropicapi.StreamEventContentBlockDelta, Index: 0, Delta: &anthropicapi.ContentDelta{Type: "signature_delta", Signature: "sig-"}},
		{Type: anthropicapi.StreamEventContentBlockDelta, Index: 0, Delta: &anthropicapi.ContentDelta{Type: "signature_delta", Signature: "123"}},
		{Type: anthropicapi.StreamEventContentBlockStop, Index: 0},
		{Type: anthropicapi.StreamEventContentBlockStart, Index: 1, Block: &core.ContentBlock{Type: core.ContentBlockText, Text: "Answer: "}},
		{Type: anthropicapi.StreamEventContentBlockDelta, Index: 1, Delta: &anthropicapi.ContentDelta{Type: "text_delta", Text: "forty"}},
		{Type: anthropicapi.StreamEventContentBlockDelta, Index: 1, Delta: &anthropicapi.ContentDelta{Type: "text_delta", Text: "-two"}},
		{Type: anthropicapi.StreamEventContentBlockStop, Index: 1},
		{Type: anthropicapi.StreamEventContentBlockStart, Index: 2, Block: &core.ContentBlock{Type: core.ContentBlockToolUse, ID: "toolu_1", Name: "lookup", Input: json.RawMessage(`{}`)}},
		{Type: anthropicapi.StreamEventContentBlockDelta, Index: 2, Delta: &anthropicapi.ContentDelta{Type: "input_json_delta", PartialJSON: `{"query":"a`}},
		{Type: anthropicapi.StreamEventContentBlockDelta, Index: 2, Delta: &anthropicapi.ContentDelta{Type: "input_json_delta", PartialJSON: `\/b","n":`}},
		{Type: anthropicapi.StreamEventContentBlockDelta, Index: 2, Delta: &anthropicapi.ContentDelta{Type: "input_json_delta", PartialJSON: `2}`}},
		{Type: anthropicapi.StreamEventContentBlockStop, Index: 2},
		{
			Type:         anthropicapi.StreamEventMessageDelta,
			MessageDelta: &anthropicapi.MessageDelta{StopReason: core.StopReasonToolUse},
			Usage: &core.Usage{
				InputTokens:              999,
				OutputTokens:             21,
				CacheCreationInputTokens: 999,
				CacheReadInputTokens:     999,
			},
		},
		{Type: anthropicapi.StreamEventMessageStop},
	}
	addAll(t, assembler, events...)
	if err := assembler.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	got, err := assembler.Response()
	if err != nil {
		t.Fatalf("Response() error = %v", err)
	}
	if got.ID != "msg_123" || got.Model != core.ModelClaudeOpus48 || got.Role != core.RoleAssistant {
		t.Fatalf("response identity = %#v", got)
	}
	wantUsage := core.Usage{
		InputTokens:              11,
		OutputTokens:             21,
		CacheCreationInputTokens: 12,
		CacheReadInputTokens:     13,
	}
	if got.Usage != wantUsage {
		t.Fatalf("usage = %#v, want %#v", got.Usage, wantUsage)
	}
	if got.StopReason != core.StopReasonToolUse || got.StopSequence != "" {
		t.Fatalf("stop metadata = %q, %q", got.StopReason, got.StopSequence)
	}
	if len(got.Content) != 3 {
		t.Fatalf("content length = %d", len(got.Content))
	}
	if got.Content[0].Type != core.ContentBlockThinking || got.Content[0].Thinking != "look closely" || got.Content[0].Signature != "sig-123" {
		t.Fatalf("thinking block = %#v", got.Content[0])
	}
	if got.Content[1].Type != core.ContentBlockText || got.Content[1].Text != "Answer: forty-two" {
		t.Fatalf("text block = %#v", got.Content[1])
	}
	tool := got.Content[2]
	if tool.Type != core.ContentBlockToolUse || tool.ID != "toolu_1" || tool.Name != "lookup" {
		t.Fatalf("tool block = %#v", tool)
	}
	if string(tool.Input) != `{"query":"a\/b","n":2}` {
		t.Fatalf("raw tool input = %q", tool.Input)
	}
	var input map[string]any
	if err := json.Unmarshal(tool.Input, &input); err != nil {
		t.Fatalf("tool input is invalid: %v", err)
	}
	if !reflect.DeepEqual(input, map[string]any{"query": "a/b", "n": float64(2)}) {
		t.Fatalf("decoded tool input = %#v", input)
	}
}

func TestStreamAssemblerSupportsInterleavedIndexedBlocks(t *testing.T) {
	assembler := NewStreamAssembler()
	tool := core.ContentBlock{Type: core.ContentBlockToolUse, ID: "toolu_1", Name: "lookup"}
	addAll(t, assembler,
		messageStartEvent(),
		textStartEvent(0),
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStart, Index: 1, Block: &tool},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockDelta, Index: 1, Delta: &anthropicapi.ContentDelta{Type: "input_json_delta", PartialJSON: `{"path":"a`}},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockDelta, Index: 0, Delta: &anthropicapi.ContentDelta{Type: "text_delta", Text: "before"}},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockDelta, Index: 1, Delta: &anthropicapi.ContentDelta{Type: "input_json_delta", PartialJSON: `\/b"}`}},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStop, Index: 1},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStart, Index: 2, Block: &core.ContentBlock{Type: core.ContentBlockText}},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockDelta, Index: 2, Delta: &anthropicapi.ContentDelta{Type: "text_delta", Text: "after"}},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStop, Index: 2},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStop, Index: 0},
		messageDeltaEvent(core.StopReasonToolUse),
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventMessageStop},
	)
	if err := assembler.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	response, err := assembler.Response()
	if err != nil {
		t.Fatalf("Response() error = %v", err)
	}
	if len(response.Content) != 3 || response.Content[0].Text != "before" || response.Content[2].Text != "after" {
		t.Fatalf("ordered content = %#v", response.Content)
	}
	if got := string(response.Content[1].Input); got != `{"path":"a\/b"}` {
		t.Fatalf("raw tool input = %q", got)
	}
}

func TestStreamAssemblerAllowsEmptyEndTurn(t *testing.T) {
	assembler := NewStreamAssembler()
	addAll(t, assembler,
		messageStartEvent(),
		anthropicapi.StreamEvent{
			Type:         anthropicapi.StreamEventMessageDelta,
			MessageDelta: &anthropicapi.MessageDelta{StopReason: core.StopReasonEndTurn},
			Usage:        &core.Usage{OutputTokens: 0},
		},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventMessageStop},
	)
	if err := assembler.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	response, err := assembler.Response()
	if err != nil {
		t.Fatalf("Response() error = %v", err)
	}
	if len(response.Content) != 0 || response.StopReason != core.StopReasonEndTurn {
		t.Fatalf("response = %#v", response)
	}
}

func TestStreamAssemblerPreservesCompleteStartBlocks(t *testing.T) {
	assembler := NewStreamAssembler()
	redacted := core.ContentBlock{Type: core.ContentBlockRedactedThinking, Data: "opaque"}
	search := core.ContentBlock{
		Type:      core.ContentBlockWebSearchToolResult,
		ToolUseID: "srv_1",
		Content: []core.ContentBlock{{
			Type:  core.ContentBlockWebSearchResult,
			Title: "Go",
			URL:   "https://go.dev",
		}},
	}
	addAll(t, assembler,
		messageStartEvent(),
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStart, Index: 0, Block: &redacted},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStop, Index: 0},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStart, Index: 1, Block: &search},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStop, Index: 1},
		messageDeltaEvent(core.StopReasonEndTurn),
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventMessageStop},
	)
	if err := assembler.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	response, err := assembler.Response()
	if err != nil {
		t.Fatalf("Response() error = %v", err)
	}
	if len(response.Content) != 2 || response.Content[0].Data != "opaque" || response.Content[1].Content[0].Title != "Go" {
		t.Fatalf("content = %#v", response.Content)
	}
}

func TestStreamAssemblerRejectsLifecycleViolations(t *testing.T) {
	tests := []struct {
		name   string
		events []anthropicapi.StreamEvent
	}{
		{name: "delta before message start", events: []anthropicapi.StreamEvent{{Type: anthropicapi.StreamEventContentBlockDelta, Index: 0, Delta: &anthropicapi.ContentDelta{Type: "text_delta", Text: "x"}}}},
		{name: "duplicate message start", events: []anthropicapi.StreamEvent{messageStartEvent(), messageStartEvent()}},
		{name: "block without payload", events: []anthropicapi.StreamEvent{messageStartEvent(), {Type: anthropicapi.StreamEventContentBlockStart, Index: 0}}},
		{name: "nonsequential block index", events: []anthropicapi.StreamEvent{messageStartEvent(), textStartEvent(1)}},
		{name: "duplicate block start", events: []anthropicapi.StreamEvent{messageStartEvent(), textStartEvent(0), {Type: anthropicapi.StreamEventContentBlockStop, Index: 0}, textStartEvent(0)}},
		{name: "wrong delta index", events: []anthropicapi.StreamEvent{messageStartEvent(), textStartEvent(0), {Type: anthropicapi.StreamEventContentBlockDelta, Index: 1, Delta: &anthropicapi.ContentDelta{Type: "text_delta", Text: "x"}}}},
		{name: "wrong stop index", events: []anthropicapi.StreamEvent{messageStartEvent(), textStartEvent(0), {Type: anthropicapi.StreamEventContentBlockStop, Index: 1}}},
		{name: "duplicate stop", events: []anthropicapi.StreamEvent{messageStartEvent(), textStartEvent(0), {Type: anthropicapi.StreamEventContentBlockStop, Index: 0}, {Type: anthropicapi.StreamEventContentBlockStop, Index: 0}}},
		{name: "message delta inside block", events: []anthropicapi.StreamEvent{messageStartEvent(), textStartEvent(0), messageDeltaEvent(core.StopReasonEndTurn)}},
		{name: "message stop before delta", events: []anthropicapi.StreamEvent{messageStartEvent(), {Type: anthropicapi.StreamEventMessageStop}}},
		{name: "content after message delta", events: []anthropicapi.StreamEvent{messageStartEvent(), messageDeltaEvent(core.StopReasonEndTurn), textStartEvent(0)}},
		{name: "duplicate message delta", events: []anthropicapi.StreamEvent{messageStartEvent(), messageDeltaEvent(core.StopReasonEndTurn), messageDeltaEvent(core.StopReasonEndTurn)}},
		{name: "event after message stop", events: []anthropicapi.StreamEvent{messageStartEvent(), messageDeltaEvent(core.StopReasonEndTurn), {Type: anthropicapi.StreamEventMessageStop}, {Type: anthropicapi.StreamEventMessageStop}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assembler := NewStreamAssembler()
			var got error
			for _, event := range tt.events {
				if got = assembler.Add(event); got != nil {
					break
				}
			}
			if !errors.Is(got, ErrInvalidStream) {
				t.Fatalf("Add() error = %v, want ErrInvalidStream", got)
			}
			if err := assembler.Finish(); !errors.Is(err, ErrInvalidStream) {
				t.Fatalf("Finish() error = %v, want latched ErrInvalidStream", err)
			}
			if _, err := assembler.Response(); !errors.Is(err, ErrInvalidStream) {
				t.Fatalf("Response() error = %v, want ErrInvalidStream", err)
			}
		})
	}
}

func TestStreamAssemblerRejectsMalformedEvents(t *testing.T) {
	streamFailure := errors.New("connection reset")
	tests := []struct {
		name   string
		events []anthropicapi.StreamEvent
	}{
		{name: "nil message", events: []anthropicapi.StreamEvent{{Type: anthropicapi.StreamEventMessageStart}}},
		{name: "wrong role", events: []anthropicapi.StreamEvent{{Type: anthropicapi.StreamEventMessageStart, Message: &anthropicapi.MessageResponse{Role: core.RoleUser}}}},
		{name: "start content", events: []anthropicapi.StreamEvent{{Type: anthropicapi.StreamEventMessageStart, Message: &anthropicapi.MessageResponse{Role: core.RoleAssistant, Content: []core.ContentBlock{core.TextBlock("x")}}}}},
		{name: "unknown event", events: []anthropicapi.StreamEvent{{Type: "ping"}}},
		{name: "error event", events: []anthropicapi.StreamEvent{{Type: anthropicapi.StreamEventError, Error: streamFailure}}},
		{name: "nil error event", events: []anthropicapi.StreamEvent{{Type: anthropicapi.StreamEventError}}},
		{name: "unknown block", events: []anthropicapi.StreamEvent{messageStartEvent(), {Type: anthropicapi.StreamEventContentBlockStart, Index: 0, Block: &core.ContentBlock{Type: "future_block"}}}},
		{name: "nil delta", events: []anthropicapi.StreamEvent{messageStartEvent(), textStartEvent(0), {Type: anthropicapi.StreamEventContentBlockDelta, Index: 0}}},
		{name: "unknown delta", events: []anthropicapi.StreamEvent{messageStartEvent(), textStartEvent(0), {Type: anthropicapi.StreamEventContentBlockDelta, Index: 0, Delta: &anthropicapi.ContentDelta{Type: "future_delta"}}}},
		{name: "delta block mismatch", events: []anthropicapi.StreamEvent{messageStartEvent(), textStartEvent(0), {Type: anthropicapi.StreamEventContentBlockDelta, Index: 0, Delta: &anthropicapi.ContentDelta{Type: "thinking_delta", Thinking: "x"}}}},
		{name: "nil message delta", events: []anthropicapi.StreamEvent{messageStartEvent(), {Type: anthropicapi.StreamEventMessageDelta, Usage: &core.Usage{}}}},
		{name: "unknown stop reason", events: []anthropicapi.StreamEvent{messageStartEvent(), messageDeltaEvent("future_reason")}},
		{name: "nil usage", events: []anthropicapi.StreamEvent{messageStartEvent(), {Type: anthropicapi.StreamEventMessageDelta, MessageDelta: &anthropicapi.MessageDelta{StopReason: core.StopReasonEndTurn}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assembler := NewStreamAssembler()
			var got error
			for _, event := range tt.events {
				if got = assembler.Add(event); got != nil {
					break
				}
			}
			if !errors.Is(got, ErrInvalidStream) {
				t.Fatalf("Add() error = %v, want ErrInvalidStream", got)
			}
			if tt.name == "error event" && !errors.Is(got, streamFailure) {
				t.Fatalf("Add() error = %v, want wrapped stream failure", got)
			}
		})
	}
}

func TestStreamAssemblerValidatesToolJSON(t *testing.T) {
	tests := []struct {
		name  string
		input []string
	}{
		{name: "empty", input: nil},
		{name: "truncated", input: []string{`{"query":`}},
		{name: "nonobject", input: []string{`[1,2]`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assembler := NewStreamAssembler()
			if err := assembler.Add(messageStartEvent()); err != nil {
				t.Fatalf("message start: %v", err)
			}
			block := core.ContentBlock{Type: core.ContentBlockToolUse, ID: "toolu_1", Name: "lookup"}
			if err := assembler.Add(anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStart, Index: 0, Block: &block}); err != nil {
				t.Fatalf("block start: %v", err)
			}
			for _, chunk := range tt.input {
				if err := assembler.Add(anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockDelta, Index: 0, Delta: &anthropicapi.ContentDelta{Type: "input_json_delta", PartialJSON: chunk}}); err != nil {
					t.Fatalf("input delta: %v", err)
				}
			}
			err := assembler.Add(anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStop, Index: 0})
			if !errors.Is(err, ErrInvalidStream) {
				t.Fatalf("block stop error = %v, want ErrInvalidStream", err)
			}
		})
	}
}

func TestStreamAssemblerUsesStartToolJSONWithoutDeltas(t *testing.T) {
	assembler := NewStreamAssembler()
	block := core.ContentBlock{Type: core.ContentBlockToolUse, ID: "toolu_1", Name: "lookup", Input: json.RawMessage(`{"ready":true}`)}
	addAll(t, assembler,
		messageStartEvent(),
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStart, Index: 0, Block: &block},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStop, Index: 0},
		messageDeltaEvent(core.StopReasonToolUse),
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventMessageStop},
	)
	if err := assembler.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	response, err := assembler.Response()
	if err != nil {
		t.Fatalf("Response() error = %v", err)
	}
	if string(response.Content[0].Input) != `{"ready":true}` {
		t.Fatalf("tool input = %q", response.Content[0].Input)
	}
}

func TestStreamAssemblerRequiresFinishAndCompleteClosure(t *testing.T) {
	t.Run("response before finish", func(t *testing.T) {
		assembler := NewStreamAssembler()
		if _, err := assembler.Response(); !errors.Is(err, ErrIncompleteStream) {
			t.Fatalf("Response() error = %v, want ErrIncompleteStream", err)
		}
	})

	incomplete := []struct {
		name   string
		events []anthropicapi.StreamEvent
	}{
		{name: "no events"},
		{name: "only start", events: []anthropicapi.StreamEvent{messageStartEvent()}},
		{name: "open block", events: []anthropicapi.StreamEvent{messageStartEvent(), textStartEvent(0)}},
		{name: "missing message stop", events: []anthropicapi.StreamEvent{messageStartEvent(), messageDeltaEvent(core.StopReasonEndTurn)}},
	}
	for _, tt := range incomplete {
		t.Run(tt.name, func(t *testing.T) {
			assembler := NewStreamAssembler()
			addAll(t, assembler, tt.events...)
			if err := assembler.Finish(); !errors.Is(err, ErrIncompleteStream) {
				t.Fatalf("Finish() error = %v, want ErrIncompleteStream", err)
			}
			if _, err := assembler.Response(); !errors.Is(err, ErrIncompleteStream) {
				t.Fatalf("Response() error = %v, want ErrIncompleteStream", err)
			}
		})
	}
}

func TestStreamAssemblerFinishIsIdempotentAndResponseIsDefensive(t *testing.T) {
	assembler := NewStreamAssembler()
	addAll(t, assembler,
		messageStartEvent(),
		textStartEvent(0),
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockDelta, Index: 0, Delta: &anthropicapi.ContentDelta{Type: "text_delta", Text: "hello"}},
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventContentBlockStop, Index: 0},
		messageDeltaEvent(core.StopReasonEndTurn),
		anthropicapi.StreamEvent{Type: anthropicapi.StreamEventMessageStop},
	)
	if err := assembler.Finish(); err != nil {
		t.Fatalf("first Finish() error = %v", err)
	}
	if err := assembler.Finish(); err != nil {
		t.Fatalf("second Finish() error = %v", err)
	}
	first, err := assembler.Response()
	if err != nil {
		t.Fatalf("first Response() error = %v", err)
	}
	first.Content[0].Text = "changed"
	second, err := assembler.Response()
	if err != nil {
		t.Fatalf("second Response() error = %v", err)
	}
	if second.Content[0].Text != "hello" {
		t.Fatalf("response was mutated through returned copy: %#v", second.Content[0])
	}
	if err := assembler.Add(anthropicapi.StreamEvent{Type: anthropicapi.StreamEventMessageStop}); !errors.Is(err, ErrInvalidStream) {
		t.Fatalf("Add() after finish error = %v, want ErrInvalidStream", err)
	}
}

func addAll(t *testing.T, assembler *StreamAssembler, events ...anthropicapi.StreamEvent) {
	t.Helper()
	for i, event := range events {
		if err := assembler.Add(event); err != nil {
			t.Fatalf("Add(event %d, %q) error = %v", i, event.Type, err)
		}
	}
}

func messageStartEvent() anthropicapi.StreamEvent {
	return anthropicapi.StreamEvent{
		Type: anthropicapi.StreamEventMessageStart,
		Message: &anthropicapi.MessageResponse{
			ID:    "msg_test",
			Model: core.ModelClaudeOpus48,
			Role:  core.RoleAssistant,
			Usage: core.Usage{InputTokens: 3},
		},
	}
}

func textStartEvent(index int) anthropicapi.StreamEvent {
	return anthropicapi.StreamEvent{
		Type:  anthropicapi.StreamEventContentBlockStart,
		Index: index,
		Block: &core.ContentBlock{Type: core.ContentBlockText},
	}
}

func messageDeltaEvent(reason core.StopReason) anthropicapi.StreamEvent {
	return anthropicapi.StreamEvent{
		Type:         anthropicapi.StreamEventMessageDelta,
		MessageDelta: &anthropicapi.MessageDelta{StopReason: reason},
		Usage:        &core.Usage{OutputTokens: 5},
	}
}
