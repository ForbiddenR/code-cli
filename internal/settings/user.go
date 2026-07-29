// Package settings loads the reduced Claude Code settings supported by code-cli.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// User contains the supported subset of the user settings file.
type User struct {
	Env map[string]string
}

// LoadUser reads the user settings file at path. A missing or empty file is
// equivalent to empty settings.
func LoadUser(path string) (User, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return User{}, nil
	}
	if err != nil {
		return User{}, fmt.Errorf("read %q: %w", path, err)
	}
	if strings.TrimSpace(string(content)) == "" {
		return User{}, nil
	}

	settings, err := parseUser(content)
	if err != nil {
		return User{}, fmt.Errorf("parse %q: %w", path, err)
	}
	return settings, nil
}

func parseUser(content []byte) (User, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	var root map[string]json.RawMessage
	if err := decoder.Decode(&root); err != nil {
		return User{}, fmt.Errorf("decode JSON: %w", err)
	}
	if root == nil {
		return User{}, errors.New("settings root must be an object")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return User{}, err
	}

	rawEnv, ok := root["env"]
	if !ok {
		return User{}, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(rawEnv, &values); err != nil || values == nil {
		return User{}, errors.New("env must be an object")
	}

	environment := make(map[string]string, len(values))
	for name, raw := range values {
		value, err := coerceEnvironmentValue(raw)
		if err != nil {
			return User{}, fmt.Errorf("env.%s: %w", name, err)
		}
		environment[name] = value
	}
	return User{Env: environment}, nil
}

func coerceEnvironmentValue(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", errors.New("value is missing")
	}

	switch trimmed[0] {
	case '"':
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return "", errors.New("value must be valid JSON")
		}
		return value, nil
	case 't', 'f':
		var value bool
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return "", errors.New("value must be valid JSON")
		}
		return strconv.FormatBool(value), nil
	case 'n':
		if bytes.Equal(trimmed, []byte("null")) {
			return "null", nil
		}
		return "", errors.New("value must be valid JSON")
	case '[', '{':
		return "", errors.New("value must be a string, number, boolean, or null")
	default:
		var value float64
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return "", errors.New("value must be a string, number, boolean, or null")
		}
		return formatJSONNumber(value), nil
	}
}

func formatJSONNumber(value float64) string {
	if value == 0 {
		return "0"
	}
	absolute := value
	if absolute < 0 {
		absolute = -absolute
	}
	if absolute >= 1e-6 && absolute < 1e21 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}

	formatted := strconv.FormatFloat(value, 'e', -1, 64)
	mantissa, exponent, found := strings.Cut(formatted, "e")
	if !found {
		return formatted
	}
	sign := ""
	if strings.HasPrefix(exponent, "+") ||
		strings.HasPrefix(exponent, "-") {
		sign = exponent[:1]
		exponent = exponent[1:]
	}
	exponent = strings.TrimLeft(exponent, "0")
	if exponent == "" {
		exponent = "0"
	}
	return mantissa + "e" + sign + exponent
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return errors.New("settings contain multiple JSON values")
}
