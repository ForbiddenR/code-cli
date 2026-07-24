package anthropicapi

import (
	"encoding/json"
	"testing"

	"code-cli/internal/core"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestNormalizeStreamEventContentBlockDelta(t *testing.T) {
	event := streamEventFromJSON(t, `{
		"type":"content_block_delta",
		"index":2,
		"delta":{"type":"input_json_delta","partial_json":"{\"city\""}
	}`)

	got, err := normalizeStreamEvent(event)
	if err != nil {
		t.Fatalf("normalizeStreamEvent() error = %v", err)
	}
	if got.Type != StreamEventContentBlockDelta || got.Index != 2 {
		t.Fatalf("event identity = %#v", got)
	}
	if got.Delta == nil || got.Delta.Type != "input_json_delta" || got.Delta.PartialJSON != `{"city"` {
		t.Fatalf("delta = %#v", got.Delta)
	}
}

func TestNormalizeStreamEventMessageDeltaPreservesUsage(t *testing.T) {
	event := streamEventFromJSON(t, `{
		"type":"message_delta",
		"delta":{"stop_reason":"end_turn","stop_sequence":"DONE"},
		"usage":{
			"input_tokens":1,
			"output_tokens":2,
			"cache_creation_input_tokens":3,
			"cache_read_input_tokens":4
		}
	}`)

	got, err := normalizeStreamEvent(event)
	if err != nil {
		t.Fatalf("normalizeStreamEvent() error = %v", err)
	}
	if got.Type != StreamEventMessageDelta {
		t.Fatalf("type = %q", got.Type)
	}
	if got.MessageDelta == nil || got.MessageDelta.StopReason != core.StopReasonEndTurn || got.MessageDelta.StopSequence != "DONE" {
		t.Fatalf("message delta = %#v", got.MessageDelta)
	}
	if got.Usage == nil || got.Usage.CacheCreationInputTokens != 3 || got.Usage.CacheReadInputTokens != 4 {
		t.Fatalf("usage = %#v", got.Usage)
	}
}

func TestNormalizeStreamEventContentBlockStart(t *testing.T) {
	event := streamEventFromJSON(t, `{
		"type":"content_block_start",
		"index":0,
		"content_block":{"type":"text","text":"hello"}
	}`)

	got, err := normalizeStreamEvent(event)
	if err != nil {
		t.Fatalf("normalizeStreamEvent() error = %v", err)
	}
	if got.Type != StreamEventContentBlockStart || got.Index != 0 {
		t.Fatalf("event identity = %#v", got)
	}
	if got.Block == nil || got.Block.Type != core.ContentBlockText || got.Block.Text != "hello" {
		t.Fatalf("block = %#v", got.Block)
	}
}

func TestNormalizeStreamEventWebSearchResultBlock(t *testing.T) {
	event := streamEventFromJSON(t, `{
		"type":"content_block_start",
		"index":1,
		"content_block":{
			"type":"web_search_tool_result",
			"tool_use_id":"srv_1",
			"content":[{"type":"web_search_result","title":"Go","url":"https://go.dev"}]
		}
	}`)

	got, err := normalizeStreamEvent(event)
	if err != nil {
		t.Fatalf("normalizeStreamEvent() error = %v", err)
	}
	if got.Block == nil || got.Block.Type != core.ContentBlockWebSearchToolResult || got.Block.ToolUseID != "srv_1" {
		t.Fatalf("block = %#v", got.Block)
	}
	if len(got.Block.Content) != 1 || got.Block.Content[0].Title != "Go" || got.Block.Content[0].URL != "https://go.dev" {
		t.Fatalf("nested search result = %#v", got.Block.Content)
	}
}

func streamEventFromJSON(t *testing.T, data string) anthropic.MessageStreamEventUnion {
	t.Helper()
	var event anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		t.Fatalf("unmarshal stream event: %v", err)
	}
	return event
}
