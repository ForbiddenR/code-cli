package skill

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"code-cli/internal/core"

	"go.yaml.in/yaml/v4"
)

type metadata struct {
	description            string
	whenToUse              string
	allowedTools           []string
	model                  string
	effort                 *core.Effort
	disableModelInvocation bool
	argumentNames          []string
	forked                 bool
}

func parseFrontmatter(content string) (metadata, string, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if normalized != "---" && !strings.HasPrefix(normalized, "---\n") {
		return metadata{}, normalized, nil
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
		return metadata{}, "", errors.New("unterminated frontmatter")
	}
	var values map[string]any
	frontmatter := strings.Join(lines[1:closing], "\n")
	if err := yaml.Unmarshal([]byte(frontmatter), &values); err != nil {
		return metadata{}, "", fmt.Errorf("parse frontmatter: %w", err)
	}
	if values == nil {
		values = map[string]any{}
	}
	parsed, err := metadataFromMap(values)
	if err != nil {
		return metadata{}, "", err
	}
	return parsed, strings.Join(lines[closing+1:], "\n"), nil
}

func metadataFromMap(values map[string]any) (metadata, error) {
	var result metadata
	var err error
	if result.description, err = optionalString(values, "description"); err != nil {
		return metadata{}, err
	}
	if result.whenToUse, err = optionalString(values, "when_to_use"); err != nil {
		return metadata{}, err
	}
	if result.model, err = optionalString(values, "model"); err != nil {
		return metadata{}, err
	}
	if result.model == "inherit" {
		result.model = ""
	}
	if result.allowedTools, err = optionalStringList(values, "allowed-tools", true); err != nil {
		return metadata{}, err
	}
	if result.argumentNames, err = optionalStringList(values, "arguments", false); err != nil {
		return metadata{}, err
	}
	result.argumentNames = validArgumentNames(result.argumentNames)
	if value, ok := values["disable-model-invocation"]; ok && value != nil {
		switch typed := value.(type) {
		case bool:
			result.disableModelInvocation = typed
		case string:
			if typed != "true" && typed != "false" {
				return metadata{}, errors.New("frontmatter disable-model-invocation must be a boolean")
			}
			result.disableModelInvocation = typed == "true"
		default:
			return metadata{}, errors.New("frontmatter disable-model-invocation must be a boolean")
		}
	}
	if value, ok := values["effort"]; ok && value != nil {
		text, ok := value.(string)
		if !ok {
			return metadata{}, errors.New("frontmatter effort must be a string")
		}
		effort := core.Effort(text)
		switch effort {
		case core.EffortLow, core.EffortMedium, core.EffortHigh, core.EffortXHigh, core.EffortMax:
			result.effort = &effort
		default:
			return metadata{}, fmt.Errorf("unsupported frontmatter effort %q", text)
		}
	}
	if value, ok := values["context"]; ok && value != nil {
		text, ok := value.(string)
		if !ok {
			return metadata{}, errors.New("frontmatter context must be a string")
		}
		result.forked = text == "fork"
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

func optionalStringList(values map[string]any, key string, splitPermissions bool) ([]string, error) {
	value, ok := values[key]
	if !ok || value == nil {
		return nil, nil
	}
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
