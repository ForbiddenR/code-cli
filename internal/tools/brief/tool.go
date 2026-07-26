// Package brief implements Claude Code's local SendUserMessage (Brief) tool.
package brief

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// Output is the structured result retained for host, UI, and SDK consumers.
type Output struct {
	Message     string       `json:"message"`
	Attachments []Attachment `json:"attachments,omitempty"`
	SentAt      string       `json:"sentAt,omitempty"`
}

// ToolResultBlock is the acknowledgement payload returned to the model loop.
type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id"`
	Type      string `json:"type"`
	Content   string `json:"content"`
}

// Config supplies local dependencies for deterministic Brief execution.
type Config struct {
	Now         func() time.Time
	Getwd       func() (string, error)
	UserHomeDir func() (string, error)
	Stat        func(string) (fs.FileInfo, error)
	Uploader    Uploader
}

// Tool executes local SendUserMessage calls.
type Tool struct {
	config Config
}

// New constructs a Brief tool with standard-library defaults.
func New(config Config) *Tool {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Getwd == nil {
		config.Getwd = os.Getwd
	}
	if config.UserHomeDir == nil {
		config.UserHomeDir = os.UserHomeDir
	}
	if config.Stat == nil {
		config.Stat = os.Stat
	}
	return &Tool{config: config}
}

// Call resolves a user-visible message and optional local attachments.
func (t *Tool) Call(ctx context.Context, input Input) (Output, error) {
	if t == nil {
		return Output{}, errors.New("brief tool is nil")
	}
	if err := ValidateInput(input); err != nil {
		return Output{}, err
	}

	sentAt := t.config.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	output := Output{Message: input.Message, SentAt: sentAt}
	if len(input.Attachments) == 0 {
		return output, nil
	}

	attachments, err := resolveAttachments(ctx, input.Attachments, attachmentConfig{
		getwd:       t.config.Getwd,
		userHomeDir: t.config.UserHomeDir,
		stat:        t.config.Stat,
		uploader:    t.config.Uploader,
	})
	if err != nil {
		return Output{}, err
	}
	output.Attachments = attachments
	return output, nil
}

// MapToolResultToToolResultBlockParam returns an acknowledgement without
// duplicating the user-visible message or attachment metadata in model context.
func MapToolResultToToolResultBlockParam(output Output, toolUseID string) ToolResultBlock {
	count := len(output.Attachments)
	content := "Message delivered to user."
	if count == 1 {
		content += " (1 attachment included)"
	} else if count > 1 {
		content += fmt.Sprintf(" (%d attachments included)", count)
	}
	return ToolResultBlock{ToolUseID: toolUseID, Type: "tool_result", Content: content}
}
