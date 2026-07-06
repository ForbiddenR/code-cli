package core

import "encoding/json"

// Message is a normalized conversation message.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// UserMessage creates a user message with a single text block.
func UserMessage(text string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{TextBlock(text)}}
}

// AssistantMessage creates an assistant message from normalized content blocks.
func AssistantMessage(content []ContentBlock) Message {
	return Message{Role: RoleAssistant, Content: content}
}

// SystemBlock is normalized system prompt content.
type SystemBlock struct {
	Type         ContentBlockType `json:"type"`
	Text         string           `json:"text"`
	CacheControl *CacheControl    `json:"cache_control,omitempty"`
}

// TextSystemBlock creates a system prompt text block.
func TextSystemBlock(text string) SystemBlock {
	return SystemBlock{Type: ContentBlockText, Text: text}
}

// ToolDefinition describes a tool that Claude may request, but does not execute it.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ThinkingType identifies the thinking configuration mode.
type ThinkingType string

const (
	ThinkingAdaptive ThinkingType = "adaptive"
)

// ThinkingConfig is the normalized request thinking configuration.
type ThinkingConfig struct {
	Type ThinkingType `json:"type"`
}

// DefaultThinking returns the default thinking configuration for complicated work.
func DefaultThinking() ThinkingConfig {
	return ThinkingConfig{Type: ThinkingAdaptive}
}

// Effort controls output effort where a model supports it.
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

// OutputConfig captures response-generation options that are not content blocks.
type OutputConfig struct {
	Effort Effort `json:"effort,omitempty"`
}

// StopReason is the normalized reason a model response ended.
type StopReason string

const (
	StopReasonEndTurn      StopReason = "end_turn"
	StopReasonMaxTokens    StopReason = "max_tokens"
	StopReasonStopSequence StopReason = "stop_sequence"
	StopReasonToolUse      StopReason = "tool_use"
	StopReasonPauseTurn    StopReason = "pause_turn"
	StopReasonRefusal      StopReason = "refusal"
)
