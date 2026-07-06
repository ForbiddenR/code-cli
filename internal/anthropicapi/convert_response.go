package anthropicapi

import (
	"encoding/json"
	"fmt"

	"code-cli/internal/core"

	"github.com/anthropics/anthropic-sdk-go"
)

func normalizeMessage(message *anthropic.Message) (*MessageResponse, error) {
	if message == nil {
		return nil, fmt.Errorf("normalize message: nil message")
	}

	content, err := normalizeContentBlocks(message.Content)
	if err != nil {
		return nil, err
	}

	return &MessageResponse{
		ID:           message.ID,
		Model:        core.ModelID(message.Model),
		Role:         core.Role(message.Role),
		Content:      content,
		StopReason:   core.StopReason(message.StopReason),
		StopSequence: message.StopSequence,
		Usage:        normalizeUsage(message.Usage),
	}, nil
}

func normalizeContentBlocks(blocks []anthropic.ContentBlockUnion) ([]core.ContentBlock, error) {
	content := make([]core.ContentBlock, 0, len(blocks))
	for i, block := range blocks {
		normalized, err := normalizeContentBlock(block.RawJSON(), block)
		if err != nil {
			return nil, fmt.Errorf("normalize content block %d: %w", i, err)
		}
		content = append(content, normalized)
	}
	return content, nil
}

func normalizeStreamContentBlock(block anthropic.ContentBlockStartEventContentBlockUnion) (core.ContentBlock, error) {
	return normalizeContentBlock(block.RawJSON(), block)
}

func normalizeContentBlock(raw string, fallback any) (core.ContentBlock, error) {
	data, err := sdkJSON(raw, fallback)
	if err != nil {
		return core.ContentBlock{}, err
	}

	var wire struct {
		Type         core.ContentBlockType `json:"type"`
		Text         string                `json:"text"`
		Thinking     string                `json:"thinking"`
		Data         string                `json:"data"`
		ID           string                `json:"id"`
		Name         string                `json:"name"`
		Input        json.RawMessage       `json:"input"`
		ToolUseID    string                `json:"tool_use_id"`
		Content      json.RawMessage       `json:"content"`
		IsError      bool                  `json:"is_error"`
		Source       *core.ContentSource   `json:"source"`
		CacheControl *core.CacheControl    `json:"cache_control"`
		Signature    string                `json:"signature"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return core.ContentBlock{}, err
	}

	block := core.ContentBlock{
		Type:         wire.Type,
		Text:         wire.Text,
		Thinking:     wire.Thinking,
		Data:         wire.Data,
		ID:           wire.ID,
		Name:         wire.Name,
		Input:        wire.Input,
		ToolUseID:    wire.ToolUseID,
		IsError:      wire.IsError,
		Source:       wire.Source,
		CacheControl: wire.CacheControl,
		Signature:    wire.Signature,
	}

	if len(wire.Content) > 0 && string(wire.Content) != "null" {
		content, err := normalizeNestedContent(wire.Content)
		if err != nil {
			return core.ContentBlock{}, err
		}
		block.Content = content
	}

	return block, nil
}

func normalizeNestedContent(data json.RawMessage) ([]core.ContentBlock, error) {
	var blocks []core.ContentBlock
	if err := json.Unmarshal(data, &blocks); err == nil {
		return blocks, nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		return []core.ContentBlock{core.TextBlock(text)}, nil
	}

	// Some server-tool result blocks use object-shaped content that is outside the
	// stable normalized contract for now. Keep the parent block metadata instead of
	// failing response normalization on those less common shapes.
	return nil, nil
}

func normalizeUsage(usage anthropic.Usage) core.Usage {
	return core.Usage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	}
}

func normalizeDeltaUsage(usage anthropic.MessageDeltaUsage) core.Usage {
	return core.Usage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	}
}

func sdkJSON(raw string, fallback any) ([]byte, error) {
	if raw != "" {
		return []byte(raw), nil
	}
	return json.Marshal(fallback)
}
