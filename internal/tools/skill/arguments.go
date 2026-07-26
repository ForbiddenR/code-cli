package skill

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	indexedArgumentsPattern = regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]`)
	shorthandPattern        = regexp.MustCompile(`\$(\d+)\b`)
)

func substituteArguments(content string, args *string, names []string) string {
	if args == nil {
		return content
	}
	values := parseArguments(*args)
	usedPlaceholder := strings.Contains(content, "$ARGUMENTS") || shorthandPattern.MatchString(content)
	for index, name := range names {
		value := ""
		if index < len(values) {
			value = values[index]
		}
		var replaced bool
		content, replaced = replaceNamedArgument(content, name, value)
		usedPlaceholder = usedPlaceholder || replaced
	}
	content = indexedArgumentsPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := indexedArgumentsPattern.FindStringSubmatch(match)
		index, _ := strconv.Atoi(parts[1])
		if index < len(values) {
			return values[index]
		}
		return ""
	})
	content = shorthandPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := shorthandPattern.FindStringSubmatch(match)
		index, _ := strconv.Atoi(parts[1])
		if index < len(values) {
			return values[index]
		}
		return ""
	})
	content = strings.ReplaceAll(content, "$ARGUMENTS", *args)
	if !usedPlaceholder && *args != "" {
		content += "\n\nARGUMENTS: " + *args
	}
	return content
}

func replaceNamedArgument(content, name, value string) (string, bool) {
	if name == "" {
		return content, false
	}
	needle := "$" + name
	replaced := false
	var result strings.Builder
	for {
		index := strings.Index(content, needle)
		if index < 0 {
			result.WriteString(content)
			return result.String(), replaced
		}
		result.WriteString(content[:index])
		after := content[index+len(needle):]
		if after != "" {
			next, _ := utf8FirstRune(after)
			if next == '[' || next == '_' || unicode.IsLetter(next) || unicode.IsDigit(next) {
				result.WriteString(needle)
				content = after
				continue
			}
		}
		result.WriteString(value)
		replaced = true
		content = after
	}
}

func utf8FirstRune(value string) (rune, int) {
	for _, character := range value {
		return character, len(string(character))
	}
	return 0, 0
}

func parseArguments(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var result []string
	var current strings.Builder
	var quote rune
	escaped := false
	hadToken := false
	for _, character := range value {
		if quote == '\'' {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			hadToken = true
			continue
		}
		if escaped {
			current.WriteRune(character)
			escaped = false
			hadToken = true
			continue
		}
		if character == '\\' {
			escaped = true
			hadToken = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			hadToken = true
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			hadToken = true
			continue
		}
		if unicode.IsSpace(character) {
			if hadToken {
				result = append(result, current.String())
				current.Reset()
				hadToken = false
			}
			continue
		}
		current.WriteRune(character)
		hadToken = true
	}
	if escaped || quote != 0 {
		return strings.Fields(value)
	}
	if hadToken {
		result = append(result, current.String())
	}
	return result
}
