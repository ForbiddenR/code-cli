package skills

import (
	"path"
	"strings"
)

func matchesPathPatterns(patterns []string, relativePath string) bool {
	relativePath = strings.TrimPrefix(strings.ReplaceAll(relativePath, "\\", "/"), "./")
	matched := false
	for _, pattern := range patterns {
		negated := strings.HasPrefix(pattern, "!")
		candidate := strings.TrimPrefix(pattern, "!")
		if matchPathPattern(candidate, relativePath) {
			matched = !negated
		}
	}
	return matched
}

func matchPathPattern(pattern, relativePath string) bool {
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	patternParts := splitPath(pattern)
	pathParts := splitPath(relativePath)
	if len(patternParts) == 1 && !anchored {
		for _, part := range pathParts {
			if segmentMatches(patternParts[0], part) {
				return true
			}
		}
		return false
	}
	if anchored {
		return matchPathParts(patternParts, pathParts)
	}
	for start := 0; start <= len(pathParts); start++ {
		if matchPathParts(patternParts, pathParts[start:]) {
			return true
		}
	}
	return false
}

func matchPathParts(patterns, values []string) bool {
	if len(patterns) == 0 {
		return len(values) == 0
	}
	if patterns[0] == "**" {
		if matchPathParts(patterns[1:], values) {
			return true
		}
		return len(values) > 0 && matchPathParts(patterns, values[1:])
	}
	return len(values) > 0 && segmentMatches(patterns[0], values[0]) && matchPathParts(patterns[1:], values[1:])
}

func segmentMatches(pattern, value string) bool {
	matched, err := path.Match(pattern, value)
	return err == nil && matched
}

func splitPath(value string) []string {
	value = strings.Trim(value, "/")
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}
