package anthropicapi

import (
	"encoding/json"
	"testing"

	"code-cli/internal/core"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestNormalizeMessagePreservesContentAndUsage(t *testing.T) {
	var sdkMessage anthropic.Message
	data := []byte(`{
		"id":"msg_123",
		"type":"message",
		"role":"assistant",
		"model":"claude-opus-4-8",
		"content":[
			{"type":"text","text":"Hello"},
			{"type":"thinking","thinking":"reasoning","signature":"sig"},
			{"type":"redacted_thinking","data":"opaque"},
			{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"query":"x"}}
		],
		"stop_reason":"tool_use",
		"stop_sequence":"",
		"usage":{
			"input_tokens":10,
			"output_tokens":20,
			"cache_creation_input_tokens":30,
			"cache_read_input_tokens":40
		}
	}`)
	if err := json.Unmarshal(data, &sdkMessage); err != nil {
		t.Fatalf("unmarshal SDK message: %v", err)
	}

	got, err := normalizeMessage(&sdkMessage)
	if err != nil {
		t.Fatalf("normalizeMessage() error = %v", err)
	}

	if got.ID != "msg_123" || got.Model != core.ModelClaudeOpus48 || got.Role != core.RoleAssistant {
		t.Fatalf("unexpected response identity: %#v", got)
	}
	if got.StopReason != core.StopReasonToolUse {
		t.Fatalf("stop reason = %q", got.StopReason)
	}
	if got.Usage.CacheCreationInputTokens != 30 || got.Usage.CacheReadInputTokens != 40 {
		t.Fatalf("usage = %#v", got.Usage)
	}
	if len(got.Content) != 4 {
		t.Fatalf("content length = %d", len(got.Content))
	}
	if got.Content[0].Text != "Hello" {
		t.Fatalf("text block = %#v", got.Content[0])
	}
	if got.Content[1].Thinking != "reasoning" || got.Content[1].Signature != "sig" {
		t.Fatalf("thinking block = %#v", got.Content[1])
	}
	if got.Content[2].Data != "opaque" {
		t.Fatalf("redacted thinking block = %#v", got.Content[2])
	}
	if got.Content[3].ID != "toolu_1" || got.Content[3].Name != "lookup" {
		t.Fatalf("tool use block = %#v", got.Content[3])
	}
	if string(got.Content[3].Input) != `{"query":"x"}` {
		t.Fatalf("tool input = %s", got.Content[3].Input)
	}
}

func TestNormalizeNestedContentString(t *testing.T) {
	content, err := normalizeNestedContent(json.RawMessage(`"plain result"`))
	if err != nil {
		t.Fatalf("normalizeNestedContent() error = %v", err)
	}
	if len(content) != 1 || content[0].Type != core.ContentBlockText || content[0].Text != "plain result" {
		t.Fatalf("content = %#v", content)
	}
}
