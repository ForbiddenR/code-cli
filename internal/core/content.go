package core

import "encoding/json"

// Role is the normalized role attached to a conversation message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ContentBlockType identifies the kind of content in a normalized message.
type ContentBlockType string

const (
	ContentBlockText              ContentBlockType = "text"
	ContentBlockImage             ContentBlockType = "image"
	ContentBlockDocument          ContentBlockType = "document"
	ContentBlockToolUse           ContentBlockType = "tool_use"
	ContentBlockToolResult        ContentBlockType = "tool_result"
	ContentBlockThinking          ContentBlockType = "thinking"
	ContentBlockRedactedThinking  ContentBlockType = "redacted_thinking"
	ContentBlockServerToolUse     ContentBlockType = "server_tool_use"
	ContentBlockWebSearchResult   ContentBlockType = "web_search_result"
	ContentBlockCodeExecutionTool ContentBlockType = "code_execution_tool_result"
)

// CacheControl describes prompt-cache behavior for cacheable content blocks.
type CacheControl struct {
	Type CacheControlType `json:"type"`
}

// CacheControlType is the API cache-control type.
type CacheControlType string

const (
	CacheControlEphemeral CacheControlType = "ephemeral"
)

// ContentSource describes non-text content sources such as images or documents.
type ContentSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	URL       string `json:"url,omitempty"`
}

// ContentBlock is the normalized content unit used by the query engine and API boundary.
type ContentBlock struct {
	Type         ContentBlockType `json:"type"`
	Text         string           `json:"text,omitempty"`
	ID           string           `json:"id,omitempty"`
	Name         string           `json:"name,omitempty"`
	Input        json.RawMessage  `json:"input,omitempty"`
	ToolUseID    string           `json:"tool_use_id,omitempty"`
	Content      []ContentBlock   `json:"content,omitempty"`
	IsError      bool             `json:"is_error,omitempty"`
	Source       *ContentSource   `json:"source,omitempty"`
	CacheControl *CacheControl    `json:"cache_control,omitempty"`
	Signature    string           `json:"signature,omitempty"`
}

// TextBlock returns a normalized text content block.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: ContentBlockText, Text: text}
}

// ToolResultBlock returns a normalized tool result block.
func ToolResultBlock(toolUseID string, content []ContentBlock, isError bool) ContentBlock {
	return ContentBlock{
		Type:      ContentBlockToolResult,
		ToolUseID: toolUseID,
		Content:   content,
		IsError:   isError,
	}
}
