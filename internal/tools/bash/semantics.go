package bash

import "strings"

var codeOneInterpretations = map[string]string{
	"grep": "No matches found",
	"rg":   "No matches found",
	"find": "Some directories were inaccessible",
	"diff": "Files differ",
	"test": "Condition is false",
	"[":    "Condition is false",
}

// interpretExitCode applies a small compatibility heuristic. It is not a shell
// parser and must never be used for authorization or security decisions.
func interpretExitCode(command string, exitCode int) (string, bool) {
	if exitCode != 1 {
		return "", false
	}
	segment, ok := finalCommandSegment(command)
	if !ok {
		return "", false
	}
	words, ok := shellWords(segment)
	if !ok || len(words) == 0 {
		return "", false
	}
	index := 0
	for index < len(words) && isAssignment(words[index]) {
		index++
	}
	if index >= len(words) {
		return "", false
	}
	name := words[index]
	if slash := strings.LastIndexAny(name, `/\\`); slash >= 0 {
		name = name[slash+1:]
	}
	interpretation, ok := codeOneInterpretations[name]
	return interpretation, ok
}

func finalCommandSegment(command string) (string, bool) {
	start := 0
	quote := byte(0)
	escaped := false
	for index := 0; index < len(command); index++ {
		value := command[index]
		if escaped {
			escaped = false
			continue
		}
		if value == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if value == quote {
				quote = 0
			}
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			continue
		}
		if value == '#' || value == '`' || value == '(' || value == ')' || value == '{' || value == '}' || value == '$' && index+1 < len(command) && command[index+1] == '(' {
			return "", false
		}
		switch value {
		case ';', '\n':
			start = index + 1
		case '&', '|':
			if index+1 < len(command) && command[index+1] == value {
				start = index + 2
				index++
			} else if value == '|' {
				start = index + 1
			}
		}
	}
	if quote != 0 || escaped {
		return "", false
	}
	segment := strings.TrimSpace(command[start:])
	return segment, segment != ""
}

func shellWords(segment string) ([]string, bool) {
	var words []string
	var current strings.Builder
	quote := byte(0)
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for index := 0; index < len(segment); index++ {
		value := segment[index]
		if escaped {
			current.WriteByte(value)
			escaped = false
			continue
		}
		if value == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if value == quote {
				quote = 0
			} else {
				current.WriteByte(value)
			}
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			continue
		}
		if value == ' ' || value == '\t' || value == '\r' || value == '\n' {
			flush()
			continue
		}
		current.WriteByte(value)
	}
	if quote != 0 || escaped {
		return nil, false
	}
	flush()
	return words, true
}

func isAssignment(word string) bool {
	separator := strings.IndexByte(word, '=')
	if separator <= 0 {
		return false
	}
	for index := 0; index < separator; index++ {
		value := word[index]
		if index == 0 {
			if value != '_' && (value < 'A' || value > 'Z') && (value < 'a' || value > 'z') {
				return false
			}
		} else if value != '_' && (value < 'A' || value > 'Z') && (value < 'a' || value > 'z') && (value < '0' || value > '9') {
			return false
		}
	}
	return true
}
