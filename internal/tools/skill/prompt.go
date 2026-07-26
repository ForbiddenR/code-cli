package skill

import (
	"encoding/json"

	"code-cli/internal/core"
)

const (
	ToolName           = "Skill"
	MaxResultSizeChars = 100_000
	ToolPrompt         = `Execute a configured local skill within the main conversation.

Use the exact skill name from the host-provided list. A leading slash is accepted for compatibility. Pass optional arguments through the args field. Do not use this tool for built-in CLI commands. The tool loads instructions only; subsequent actions still require their ordinary tools and host authorization.`
)

var inputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "skill": {
      "type": "string",
      "description": "The configured local skill name, optionally prefixed by /."
    },
    "args": {
      "type": "string",
      "description": "Optional arguments for the skill."
    }
  },
  "required": ["skill"],
  "additionalProperties": false
}`)

// Definition returns the model-facing Skill declaration.
func Definition() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        ToolName,
		Description: ToolPrompt,
		InputSchema: append(json.RawMessage(nil), inputSchema...),
	}
}
