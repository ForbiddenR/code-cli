package bash

import (
	"encoding/json"

	"code-cli/internal/core"
)

const (
	// ToolName is the model-facing local shell tool name.
	ToolName = "Bash"

	// ToolPrompt describes the focused foreground shell boundary retained here.
	ToolPrompt = "Executes a shell command in a local foreground process.\n\n" +
		"Usage:\n" +
		"- Prefer dedicated tools for file search, reading, and editing when they are available.\n" +
		"- Use `&&` when later commands should run only after earlier commands succeed; use `;` when they should run regardless.\n" +
		"- Commands start in the host-configured working directory. `cd`, exported variables, aliases, and functions apply only within the current command and do not persist to later calls.\n" +
		"- Commands run with `bash -c` by default without automatically sourcing login profile files.\n" +
		"- The default timeout is 120000 ms and the maximum timeout is 600000 ms.\n" +
		"- Avoid destructive or hard-to-reverse operations. Do not amend commits, force-push, or change git configuration unless the user explicitly requests it.\n" +
		"- This tool executes with the privileges of the host process. The host must authorize the call before invoking it.\n"
)

var inputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The command to execute"
    },
    "timeout": {
      "type": "number",
      "description": "Optional timeout in milliseconds (max 600000)"
    },
    "description": {
      "type": "string",
      "description": "Clear, concise description of what this command does in active voice. Never use words like \"complex\" or \"risk\" in the description - just describe what it does.\n\nFor simple commands (git, npm, standard CLI tools), keep it brief (5-10 words):\n- ls → \"List files in current directory\"\n- git status → \"Show working tree status\"\n- npm install → \"Install package dependencies\"\n\nFor commands that are harder to parse at a glance (piped commands, obscure flags, etc.), add enough context to clarify what it does:\n- find . -name \"*.tmp\" -exec rm {} \\; → \"Find and delete all .tmp files recursively\"\n- git reset --hard origin/main → \"Discard all local changes and match remote main\"\n- curl -s url | jq '.data[]' → \"Fetch JSON from URL and extract data array elements\""
    }
  },
  "required": ["command"],
  "additionalProperties": false
}`)

// Definition returns the canonical custom-tool declaration.
func Definition() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        ToolName,
		Description: ToolPrompt,
		InputSchema: append(json.RawMessage(nil), inputSchema...),
	}
}
