package brief

import (
	"encoding/json"

	"code-cli/internal/core"
)

const (
	// ToolName is the canonical model-facing Brief tool name.
	ToolName = "SendUserMessage"
	// LegacyToolName is retained for transcript and caller compatibility.
	LegacyToolName = "Brief"
	// Description is the short model-facing tool description.
	Description = "Send a message to the user"

	// ToolPrompt explains how Claude should use SendUserMessage.
	ToolPrompt = "Send a message the user will read. Text outside this tool is visible in the detail view, but most won't open it — the answer lives here.\n\n" +
		"`message` supports markdown. `attachments` takes file paths (absolute or cwd-relative) for images, diffs, logs.\n\n" +
		"`status` labels intent: 'normal' when replying to what they just asked; 'proactive' when you're initiating — a scheduled task finished, a blocker surfaced during background work, you need input on something they haven't asked about. Set it honestly; downstream routing uses it."

	// SystemPrompt is the associated Brief-mode system-prompt section.
	SystemPrompt = "## Talking to the user\n\n" +
		"SendUserMessage is where your replies go. Text outside it is visible if the user expands the detail view, but most won't — assume unread. Anything you want them to actually see goes through SendUserMessage. The failure mode: the real answer lives in plain text while SendUserMessage just says \"done!\" — they see \"done!\" and miss everything.\n\n" +
		"So: every time the user says something, the reply they actually read comes through SendUserMessage. Even for \"hi\". Even for \"thanks\".\n\n" +
		"If you can answer right away, send the answer. If you need to go look — run a command, read files, check something — ack first in one line (\"On it — checking the test output\"), then work, then send the result. Without the ack they're staring at a spinner.\n\n" +
		"For longer work: ack → work → result. Between those, send a checkpoint when something useful happened — a decision you made, a surprise you hit, a phase boundary. Skip the filler (\"running tests...\") — a checkpoint earns its place by carrying information.\n\n" +
		"Keep messages tight — the decision, the file:line, the PR number. Second person always (\"your config\"), never third."
)

var inputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "message": {
      "type": "string",
      "description": "The message for the user. Supports markdown formatting."
    },
    "attachments": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Optional file paths (absolute or relative to cwd) to attach. Use for photos, screenshots, diffs, logs, or any file the user should see alongside your message."
    },
    "status": {
      "type": "string",
      "enum": ["normal", "proactive"],
      "description": "Use 'proactive' when you're surfacing something the user hasn't asked for and needs to see now — task completion while they're away, a blocker you hit, an unsolicited status update. Use 'normal' when replying to something the user just said."
    }
  },
  "required": ["message", "status"],
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

// Aliases returns the accepted legacy model-facing names.
func Aliases() []string {
	return []string{LegacyToolName}
}

// MatchesName reports whether name identifies the canonical or legacy tool.
func MatchesName(name string) bool {
	return name == ToolName || name == LegacyToolName
}
