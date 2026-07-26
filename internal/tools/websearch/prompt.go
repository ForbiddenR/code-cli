package websearch

import (
	"encoding/json"
	"time"

	"code-cli/internal/core"
)

var inputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "minLength": 2,
      "description": "The search query to use"
    },
    "allowed_domains": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Only include search results from these domains"
    },
    "blocked_domains": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Never include search results from these domains"
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`)

// Definition returns the canonical custom-tool declaration for the supplied time.
func Definition(now time.Time) core.ToolDefinition {
	return core.ToolDefinition{
		Name:        ToolName,
		Description: Prompt(now),
		InputSchema: append(json.RawMessage(nil), inputSchema...),
	}
}
