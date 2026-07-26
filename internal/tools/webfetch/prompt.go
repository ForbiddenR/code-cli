package webfetch

import (
	"context"
	"encoding/json"
	"errors"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
)

const ToolPrompt = `Fetches content from a specified URL and processes it using an AI model.

Usage notes:
- Takes a fully formed URL and a prompt describing what information to extract.
- HTTP URLs are automatically upgraded to HTTPS.
- HTML content is converted to Markdown before prompt processing.
- A small, fast model may summarize or analyze nontrivial content.
- Results are cached for 15 minutes when the fetched content is reusable.
- Cross-host redirects are returned as instructions for a follow-up WebFetch call.
- Authenticated or private URLs are not supported.
- This tool is read-only and does not modify remote content.`

const preapprovedGuidelines = "Provide a concise response based on the content above. Include relevant details, code examples, and documentation excerpts as needed."

var inputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "The URL to fetch content from"
    },
    "prompt": {
      "type": "string",
      "description": "The prompt to run on the fetched content"
    }
  },
  "required": ["url", "prompt"],
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

const restrictedGuidelines = `Provide a concise response based only on the content above. In your response:
 - Enforce a strict 125-character maximum for quotes from any source document. Open Source Software is ok as long as we respect the license.
 - Use quotation marks for exact language from articles; any language outside of the quotation should never be word-for-word the same.
 - You are not a lawyer and never comment on the legality of your own prompts and responses.
 - Never produce or reproduce exact song lyrics.`

func makeSecondaryModelPrompt(content, prompt string, preapproved bool) string {
	if jsStringLength(content) > MaxMarkdownLength {
		content = truncateJSString(content, MaxMarkdownLength) + "\n\n[Content truncated due to length...]"
	}
	guidelines := restrictedGuidelines
	if preapproved {
		guidelines = preapprovedGuidelines
	}
	return "\nWeb page content:\n---\n" + content + "\n---\n\n" + prompt + "\n\n" + guidelines
}

func (t *WebFetchTool) applyPrompt(ctx context.Context, content, prompt string, preapproved bool) (string, error) {
	if t.config.Client == nil {
		return "", errors.New("web fetch model client is nil")
	}
	response, err := t.config.Client.CreateMessage(ctx, anthropicapi.MessageRequest{
		Model:     t.config.SmallModel,
		MaxTokens: t.config.MaxTokens,
		Messages:  []core.Message{core.UserMessage(makeSecondaryModelPrompt(content, prompt, preapproved))},
	})
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if response == nil || len(response.Content) == 0 || response.Content[0].Type != core.ContentBlockText {
		return "No response from model", nil
	}
	return response.Content[0].Text, nil
}

func jsStringLength(value string) int {
	length := 0
	for _, r := range value {
		if r > 0xffff {
			length += 2
		} else {
			length++
		}
	}
	return length
}

func truncateJSString(value string, limit int) string {
	length := 0
	for index, r := range value {
		units := 1
		if r > 0xffff {
			units = 2
		}
		if length+units > limit {
			return value[:index]
		}
		length += units
	}
	return value
}
