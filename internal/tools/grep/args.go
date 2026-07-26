package grep

import (
	"strconv"
	"strings"
)

var vcsDirectories = [...]string{".git", ".svn", ".hg", ".bzr", ".jj", ".sl"}

func buildArguments(input Input, target string) []string {
	mode := input.normalizedMode()
	args := []string{"--hidden"}
	for _, directory := range vcsDirectories {
		args = append(args, "--glob", "!"+directory)
	}
	args = append(args, "--max-columns", "500")
	if input.multiline() {
		args = append(args, "-U", "--multiline-dotall")
	}
	if input.caseInsensitive() {
		args = append(args, "-i")
	}
	switch mode {
	case OutputModeFilesWithMatches:
		args = append(args, "-l")
	case OutputModeCount:
		args = append(args, "-c", "--with-filename")
	case OutputModeContent:
		args = append(args, "--with-filename")
	}
	if mode == OutputModeContent && (input.lineNumbers() || input.hasContext()) {
		// Force line numbers for context searches so match/context separators are
		// unambiguous. Result processing removes them when -n was explicitly false.
		args = append(args, "-n")
	}
	if mode == OutputModeContent {
		switch {
		case input.Context != nil:
			args = append(args, "-C", intString(*input.Context))
		case input.ContextAlias != nil:
			args = append(args, "-C", intString(*input.ContextAlias))
		default:
			if input.Before != nil {
				args = append(args, "-B", intString(*input.Before))
			}
			if input.After != nil {
				args = append(args, "-A", intString(*input.After))
			}
		}
	}
	if strings.HasPrefix(input.Pattern, "-") {
		args = append(args, "-e", input.Pattern)
	} else {
		args = append(args, input.Pattern)
	}
	if input.Type != "" {
		args = append(args, "--type", input.Type)
	}
	for _, pattern := range splitGlobPatterns(input.Glob) {
		args = append(args, "--glob", pattern)
	}
	return append(args, target)
}

func splitGlobPatterns(value string) []string {
	var patterns []string
	for token := range strings.FieldsSeq(value) {
		if strings.Contains(token, "{") && strings.Contains(token, "}") {
			patterns = append(patterns, token)
			continue
		}
		for part := range strings.SplitSeq(token, ",") {
			if part != "" {
				patterns = append(patterns, part)
			}
		}
	}
	return patterns
}

func intString(value int) string {
	return strconv.Itoa(value)
}
