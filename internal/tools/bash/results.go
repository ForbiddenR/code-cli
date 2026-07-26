package bash

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Output is the structured local result of one Bash call. Callers should retain
// it even when Call also returns a non-nil execution error.
type Output struct {
	Stdout                   string `json:"stdout"`
	Stderr                   string `json:"stderr"`
	ExitCode                 int    `json:"exitCode"`
	Interrupted              bool   `json:"interrupted"`
	TimedOut                 bool   `json:"timedOut,omitempty"`
	Truncated                bool   `json:"truncated,omitempty"`
	DurationMS               int64  `json:"durationMs"`
	ReturnCodeInterpretation string `json:"returnCodeInterpretation,omitempty"`
	IsError                  bool   `json:"isError,omitempty"`
	FailureMessage           string `json:"failureMessage,omitempty"`
	OutputLimit              int    `json:"outputLimit,omitempty"`
}

// ToolResultBlock is the string-content result sent back to the model.
type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// ExecutionError describes a command that started but did not complete
// successfully.
type ExecutionError struct {
	ExitCode int
	Err      error
}

func (err *ExecutionError) Error() string {
	if err == nil {
		return "bash execution failed"
	}
	if err.ExitCode >= 0 {
		return fmt.Sprintf("bash command failed with exit code %d: %v", err.ExitCode, err.Err)
	}
	return fmt.Sprintf("bash execution failed: %v", err.Err)
}

func (err *ExecutionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

// MapToolResultToToolResultBlockParam formats structured Bash output for Claude.
func MapToolResultToToolResultBlockParam(output Output, toolUseID string) ToolResultBlock {
	content := normalizeCommandOutput(output.Stdout)
	if output.Truncated {
		content = strings.TrimSuffix(content, truncationMarker)
	}
	if content == "" {
		switch {
		case output.ReturnCodeInterpretation != "" && !output.IsError:
			content = output.ReturnCodeInterpretation
		case !output.IsError:
			content = "(Bash completed with no output)"
		case output.FailureMessage == "":
			content = "Bash command failed"
		}
	}
	annotations := make([]string, 0, 2)
	if output.FailureMessage != "" {
		annotations = append(annotations, output.FailureMessage)
	}
	if output.Truncated {
		annotations = append(annotations, "output was truncated")
	}
	content = fitContentWithAnnotations(content, annotations, output.OutputLimit)
	return ToolResultBlock{
		ToolUseID: toolUseID,
		Type:      "tool_result",
		Content:   content,
		IsError:   output.IsError,
	}
}

func normalizeCommandOutput(value string) string {
	value = strings.TrimRightFunc(value, unicode.IsSpace)
	for value != "" {
		separator := strings.IndexByte(value, '\n')
		if separator < 0 {
			if strings.TrimSpace(value) == "" {
				return ""
			}
			break
		}
		if strings.TrimSpace(value[:separator]) != "" {
			break
		}
		value = value[separator+1:]
	}
	return value
}

func fitContentWithAnnotations(content string, messages []string, limit int) string {
	annotations := make([]string, 0, len(messages))
	for _, message := range messages {
		annotations = append(annotations, "["+message+"]")
	}
	annotationText := strings.Join(annotations, "\n")
	separator := ""
	if content != "" && annotationText != "" {
		separator = "\n\n"
	}
	combined := content + separator + annotationText
	if limit <= 0 || len(combined) <= limit {
		return combined
	}
	reserved := separator + annotationText
	if len(reserved) >= limit {
		return validStringPrefix(annotationText, limit)
	}
	return validStringPrefix(content, limit-len(reserved)) + reserved
}

func validStringPrefix(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for value != "" && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
