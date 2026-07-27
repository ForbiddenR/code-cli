package skills

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"code-cli/internal/core"

	"go.yaml.in/yaml/v4"
)

func parseFrontmatter(content string) (Metadata, string, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if normalized != "---" && !strings.HasPrefix(normalized, "---\n") {
		return defaultMetadata(), normalized, nil
	}
	lines := strings.Split(normalized, "\n")
	closing := -1
	for index := 1; index < len(lines); index++ {
		if lines[index] == "---" {
			closing = index
			break
		}
	}
	if closing < 0 {
		return Metadata{}, "", errors.New("unterminated frontmatter")
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closing], "\n")), &values); err != nil {
		return Metadata{}, "", fmt.Errorf("parse frontmatter: %w", err)
	}
	if values == nil {
		values = map[string]any{}
	}
	metadata, err := metadataFromMap(values)
	if err != nil {
		return Metadata{}, "", err
	}
	return metadata, strings.Join(lines[closing+1:], "\n"), nil
}

func defaultMetadata() Metadata {
	return Metadata{UserInvocable: true, Context: "inline"}
}

func metadataFromMap(values map[string]any) (Metadata, error) {
	result := defaultMetadata()
	var err error
	if result.DisplayName, err = optionalString(values, "name"); err != nil {
		return Metadata{}, err
	}
	if result.Description, err = optionalString(values, "description"); err != nil {
		return Metadata{}, err
	}
	if result.WhenToUse, err = optionalString(values, "when_to_use"); err != nil {
		return Metadata{}, err
	}
	if result.ArgumentHint, err = optionalString(values, "argument-hint"); err != nil {
		return Metadata{}, err
	}
	if result.Version, err = optionalString(values, "version"); err != nil {
		return Metadata{}, err
	}
	if result.Model, err = optionalString(values, "model"); err != nil {
		return Metadata{}, err
	}
	if result.Model == "inherit" {
		result.Model = ""
	}
	if result.Agent, err = optionalString(values, "agent"); err != nil {
		return Metadata{}, err
	}
	if result.Shell, err = optionalString(values, "shell"); err != nil {
		return Metadata{}, err
	}

	if value, ok := values["allowed-tools"]; ok && value != nil {
		result.AllowedToolsSpecified = true
		if result.AllowedTools, err = stringList(value, "allowed-tools", true); err != nil {
			return Metadata{}, err
		}
	}
	if value, ok := values["arguments"]; ok && value != nil {
		if result.ArgumentNames, err = stringList(value, "arguments", false); err != nil {
			return Metadata{}, err
		}
		result.ArgumentNames = validArgumentNames(result.ArgumentNames)
	}
	if value, ok := values["paths"]; ok && value != nil {
		if result.Paths, err = stringList(value, "paths", false); err != nil {
			return Metadata{}, err
		}
		for index, pattern := range result.Paths {
			normalized, normalizeErr := normalizePathPattern(pattern)
			if normalizeErr != nil {
				return Metadata{}, normalizeErr
			}
			result.Paths[index] = normalized
		}
	}

	if result.DisableModelInvocation, err = optionalBool(values, "disable-model-invocation", false); err != nil {
		return Metadata{}, err
	}
	if result.UserInvocable, err = optionalBool(values, "user-invocable", true); err != nil {
		return Metadata{}, err
	}
	if value, ok := values["effort"]; ok && value != nil {
		text, ok := value.(string)
		if !ok {
			return Metadata{}, errors.New("frontmatter effort must be a string")
		}
		effort := core.Effort(strings.TrimSpace(text))
		switch effort {
		case core.EffortLow, core.EffortMedium, core.EffortHigh, core.EffortXHigh, core.EffortMax:
			result.Effort = &effort
		default:
			return Metadata{}, fmt.Errorf("unsupported frontmatter effort %q", text)
		}
	}
	if value, ok := values["context"]; ok && value != nil {
		text, ok := value.(string)
		if !ok {
			return Metadata{}, errors.New("frontmatter context must be a string")
		}
		text = strings.TrimSpace(text)
		if text != "inline" && text != "fork" {
			return Metadata{}, fmt.Errorf("unsupported frontmatter context %q", text)
		}
		result.Context = text
	}
	if value, ok := values["hooks"]; ok && value != nil {
		hooks, ok := value.(map[string]any)
		if !ok {
			return Metadata{}, errors.New("frontmatter hooks must be an object")
		}
		result.Hooks = cloneStringAnyMap(hooks)
	}
	return result, nil
}

func optionalString(values map[string]any, key string) (string, error) {
	value, ok := values[key]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("frontmatter %s must be a string", key)
	}
	return strings.TrimSpace(text), nil
}

func optionalBool(values map[string]any, key string, defaultValue bool) (bool, error) {
	value, ok := values[key]
	if !ok || value == nil {
		return defaultValue, nil
	}
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		if typed == "true" {
			return true, nil
		}
		if typed == "false" {
			return false, nil
		}
	}
	return false, fmt.Errorf("frontmatter %s must be a boolean", key)
}

func stringList(value any, key string, splitPermissions bool) ([]string, error) {
	var result []string
	switch typed := value.(type) {
	case string:
		if splitPermissions {
			result = splitAllowedTools(typed)
		} else {
			result = strings.Fields(typed)
		}
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("frontmatter %s must contain only strings", key)
			}
			text = strings.TrimSpace(text)
			if text != "" {
				result = append(result, text)
			}
		}
	default:
		return nil, fmt.Errorf("frontmatter %s must be a string or string list", key)
	}
	return deduplicate(result), nil
}

func splitAllowedTools(value string) []string {
	var result []string
	var current strings.Builder
	depth := 0
	flush := func() {
		text := strings.TrimSpace(current.String())
		if text != "" {
			result = append(result, text)
		}
		current.Reset()
	}
	for _, character := range value {
		switch character {
		case '(':
			depth++
			current.WriteRune(character)
		case ')':
			if depth > 0 {
				depth--
			}
			current.WriteRune(character)
		case ',':
			if depth == 0 {
				flush()
			} else {
				current.WriteRune(character)
			}
		default:
			if unicode.IsSpace(character) && depth == 0 {
				flush()
			} else {
				current.WriteRune(character)
			}
		}
	}
	flush()
	return result
}

func validArgumentNames(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || isDigits(value) {
			continue
		}
		result = append(result, value)
	}
	return deduplicate(result)
}

func normalizePathPattern(pattern string) (string, error) {
	pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
	negated := strings.HasPrefix(pattern, "!")
	candidate := strings.TrimPrefix(pattern, "!")
	if candidate == "" {
		return "", errors.New("frontmatter paths contains an empty pattern")
	}
	if slices.Contains(strings.Split(candidate, "/"), "..") {
		return "", fmt.Errorf("frontmatter paths pattern %q contains traversal", pattern)
	}
	if negated {
		candidate = "!" + candidate
	}
	return candidate, nil
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !unicode.IsDigit(character) {
			return false
		}
	}
	return true
}

func deduplicate(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func descriptionFromBody(body string) string {
	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return strings.TrimSpace(strings.TrimLeft(line, "#"))
	}
	return ""
}
