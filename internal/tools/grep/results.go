package grep

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const partialWarning = "[Warning: Search results are partial because ripgrep did not complete.]"

// Output is the structured local result of one Grep call.
type Output struct {
	Mode          OutputMode `json:"mode,omitempty"`
	NumFiles      int        `json:"numFiles"`
	Filenames     []string   `json:"filenames"`
	Content       string     `json:"content,omitempty"`
	NumLines      *int       `json:"numLines,omitempty"`
	NumMatches    *int       `json:"numMatches,omitempty"`
	AppliedLimit  *int       `json:"appliedLimit,omitempty"`
	AppliedOffset *int       `json:"appliedOffset,omitempty"`
	Partial       bool       `json:"partial,omitempty"`
}

// ToolResultBlock is the string-content result sent back to the model.
type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id"`
	Type      string `json:"type"`
	Content   string `json:"content"`
}

func buildOutput(input Input, stdout, cwd string, partial bool, stat func(string) (fs.FileInfo, error)) Output {
	lines := normalizeOutputLines(stdout)
	mode := input.normalizedMode()
	switch mode {
	case OutputModeContent:
		return buildContentOutput(input, lines, cwd, partial, stat)
	case OutputModeCount:
		return buildCountOutput(input, lines, cwd, partial)
	default:
		return buildFilesOutput(input, lines, cwd, partial, stat)
	}
}

func buildContentOutput(input Input, lines []string, cwd string, partial bool, stat func(string) (fs.FileInfo, error)) Output {
	page, appliedLimit := paginate(lines, input.offset(), input.headLimit())
	for index, line := range page {
		page[index] = relativizeContentLine(line, cwd, stat, input.lineNumbers(), input.hasContext())
	}
	numLines := len(page)
	return Output{
		Mode:          OutputModeContent,
		NumFiles:      0,
		Filenames:     []string{},
		Content:       strings.Join(page, "\n"),
		NumLines:      &numLines,
		AppliedLimit:  appliedLimit,
		AppliedOffset: positivePointer(input.offset()),
		Partial:       partial,
	}
}

func buildCountOutput(input Input, lines []string, cwd string, partial bool) Output {
	page, appliedLimit := paginate(lines, input.offset(), input.headLimit())
	totalMatches := 0
	fileCount := 0
	for index, line := range page {
		separator := strings.LastIndexByte(line, ':')
		if separator <= 0 {
			continue
		}
		path := line[:separator]
		countText := line[separator+1:]
		page[index] = relativizePath(path, cwd) + line[separator:]
		count, err := strconv.Atoi(countText)
		if err == nil {
			totalMatches += count
			fileCount++
		}
	}
	return Output{
		Mode:          OutputModeCount,
		NumFiles:      fileCount,
		Filenames:     []string{},
		Content:       strings.Join(page, "\n"),
		NumMatches:    &totalMatches,
		AppliedLimit:  appliedLimit,
		AppliedOffset: positivePointer(input.offset()),
		Partial:       partial,
	}
}

func buildFilesOutput(input Input, lines []string, cwd string, partial bool, stat func(string) (fs.FileInfo, error)) Output {
	type fileMatch struct {
		path  string
		mtime time.Time
	}
	matches := make([]fileMatch, len(lines))
	for index, path := range lines {
		matches[index].path = path
		if info, err := stat(path); err == nil {
			matches[index].mtime = info.ModTime()
		}
	}
	sort.SliceStable(matches, func(left, right int) bool {
		if matches[left].mtime.Equal(matches[right].mtime) {
			return matches[left].path < matches[right].path
		}
		return matches[left].mtime.After(matches[right].mtime)
	})
	sortedPaths := make([]string, len(matches))
	for index, match := range matches {
		sortedPaths[index] = match.path
	}
	page, appliedLimit := paginate(sortedPaths, input.offset(), input.headLimit())
	for index, path := range page {
		page[index] = relativizePath(path, cwd)
	}
	return Output{
		Mode:          OutputModeFilesWithMatches,
		NumFiles:      len(page),
		Filenames:     page,
		AppliedLimit:  appliedLimit,
		AppliedOffset: positivePointer(input.offset()),
		Partial:       partial,
	}
}

func normalizeOutputLines(stdout string) []string {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func paginate[T any](items []T, offset, limit int) ([]T, *int) {
	start := min(offset, len(items))
	if limit == 0 {
		return append([]T(nil), items[start:]...), nil
	}
	end := min(start+limit, len(items))
	page := append([]T(nil), items[start:end]...)
	if len(items)-start > limit {
		return page, new(limit)
	}
	return page, nil
}

func relativizeContentLine(line, cwd string, stat func(string) (fs.FileInfo, error), showLineNumbers, hasContext bool) string {
	if line == "--" {
		return line
	}
	if showLineNumbers || hasContext {
		if path, number, content, ok := findNumberedField(line, ':', stat); ok {
			separator := ":"
			if showLineNumbers {
				separator += number + ":"
			}
			return relativizePath(path, cwd) + separator + content
		}
		if path, number, content, ok := findNumberedField(line, '-', stat); ok {
			separator := "-"
			if showLineNumbers {
				separator += number + "-"
			}
			return relativizePath(path, cwd) + separator + content
		}
	}
	if separator := contentColon(line); separator > 0 {
		return relativizePath(line[:separator], cwd) + line[separator:]
	}
	return line
}

func findNumberedField(line string, separator byte, stat func(string) (fs.FileInfo, error)) (string, string, string, bool) {
	bestPath := ""
	bestNumber := ""
	bestContent := ""
	for index := 1; index < len(line); index++ {
		if line[index] != separator {
			continue
		}
		numberStart := index + 1
		numberEnd := numberStart
		for numberEnd < len(line) && line[numberEnd] >= '0' && line[numberEnd] <= '9' {
			numberEnd++
		}
		if numberEnd == numberStart || numberEnd >= len(line) || line[numberEnd] != separator {
			continue
		}
		candidate := line[:index]
		if info, err := stat(candidate); err == nil && info.Mode().IsRegular() {
			bestPath = candidate
			bestNumber = line[numberStart:numberEnd]
			bestContent = line[numberEnd+1:]
		}
	}
	return bestPath, bestNumber, bestContent, bestPath != ""
}

func contentColon(line string) int {
	start := 0
	if len(line) >= 3 && isASCIIAlpha(line[0]) && line[1] == ':' && (line[2] == '\\' || line[2] == '/') {
		start = 2
	}
	if index := strings.IndexByte(line[start:], ':'); index >= 0 {
		return start + index
	}
	return -1
}

func isASCIIAlpha(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func relativizePath(path, cwd string) string {
	if path == "" || !filepath.IsAbs(path) {
		return path
	}
	relative, err := filepath.Rel(cwd, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return path
	}
	return relative
}

func positivePointer(value int) *int {
	if value <= 0 {
		return nil
	}
	return new(value)
}

// MapToolResultToToolResultBlockParam formats structured Grep output for Claude.
func MapToolResultToToolResultBlockParam(output Output, toolUseID string) ToolResultBlock {
	var content string
	limitInfo := formatLimitInfo(output.AppliedLimit, output.AppliedOffset)
	switch output.Mode {
	case OutputModeContent:
		content = output.Content
		if content == "" {
			content = "No matches found"
		}
		if limitInfo != "" {
			content += "\n\n[Showing results with pagination = " + limitInfo + "]"
		}
	case OutputModeCount:
		content = output.Content
		if content == "" {
			content = "No matches found"
		}
		matches := pointerValue(output.NumMatches)
		content += fmt.Sprintf("\n\nFound %d total %s across %d %s.", matches, plural(matches, "occurrence"), output.NumFiles, plural(output.NumFiles, "file"))
		if limitInfo != "" {
			content += " with pagination = " + limitInfo
		}
	default:
		if output.NumFiles == 0 {
			content = "No files found"
		} else {
			content = fmt.Sprintf("Found %d %s", output.NumFiles, plural(output.NumFiles, "file"))
			if limitInfo != "" {
				content += " " + limitInfo
			}
			content += "\n" + strings.Join(output.Filenames, "\n")
		}
	}
	if output.Partial {
		content += "\n\n" + partialWarning
	}
	return ToolResultBlock{ToolUseID: toolUseID, Type: "tool_result", Content: content}
}

func formatLimitInfo(limit, offset *int) string {
	parts := make([]string, 0, 2)
	if limit != nil {
		parts = append(parts, fmt.Sprintf("limit: %d", *limit))
	}
	if offset != nil && *offset > 0 {
		parts = append(parts, fmt.Sprintf("offset: %d", *offset))
	}
	return strings.Join(parts, ", ")
}

func pointerValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func plural(count int, singular string) string {
	if count == 1 {
		return singular
	}
	return singular + "s"
}
