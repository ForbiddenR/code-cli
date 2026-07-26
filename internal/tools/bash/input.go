package bash

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const MaxTimeoutMS = 600000

// Input is the normalized Bash tool input.
type Input struct {
	Command     string   `json:"command"`
	TimeoutMS   *float64 `json:"timeout,omitempty"`
	Description string   `json:"description,omitempty"`
}

var semanticTimeoutPattern = regexp.MustCompile(`^\d+(?:\.\d+)?$`)

// ParseInput strictly decodes and normalizes one Bash tool input object.
func ParseInput(data []byte) (Input, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return Input{}, fmt.Errorf("decode bash input: %w", err)
	}
	if fields == nil {
		return Input{}, errors.New("bash input must be an object")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Input{}, err
	}
	for name := range fields {
		switch name {
		case "command", "timeout", "description":
		default:
			return Input{}, fmt.Errorf("unknown field %q", name)
		}
	}

	command, err := decodeRequiredString("command", fields["command"])
	if err != nil {
		return Input{}, err
	}
	input := Input{Command: command}
	if value := fields["description"]; value != nil {
		input.Description, err = decodeString("description", value)
		if err != nil {
			return Input{}, err
		}
	}
	if value := fields["timeout"]; value != nil {
		timeout, decodeErr := decodeSemanticTimeout(value)
		if decodeErr != nil {
			return Input{}, decodeErr
		}
		input.TimeoutMS = &timeout
	}
	if err := ValidateInput(input); err != nil {
		return Input{}, err
	}
	return input, nil
}

// ValidateInput validates typed values that did not pass through ParseInput.
func ValidateInput(input Input) error {
	if strings.IndexByte(input.Command, 0) >= 0 {
		return errors.New("command contains a NUL byte")
	}
	if input.TimeoutMS != nil {
		value := *input.TimeoutMS
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > MaxTimeoutMS {
			return fmt.Errorf("timeout must be greater than 0 and at most %d milliseconds", MaxTimeoutMS)
		}
	}
	return nil
}

func decodeRequiredString(name string, value json.RawMessage) (string, error) {
	if value == nil {
		return "", fmt.Errorf("missing required field %q", name)
	}
	return decodeString(name, value)
}

func decodeString(name string, value json.RawMessage) (string, error) {
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return "", fmt.Errorf("field %q must be a string", name)
	}
	var decoded string
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "", fmt.Errorf("field %q must be a string: %w", name, err)
	}
	return decoded, nil
}

func decodeSemanticTimeout(value json.RawMessage) (float64, error) {
	trimmed := bytes.TrimSpace(value)
	if bytes.Equal(trimmed, []byte("null")) {
		return 0, errors.New("field \"timeout\" must be a number")
	}
	text := string(trimmed)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var decoded string
		if err := json.Unmarshal(trimmed, &decoded); err != nil {
			return 0, errors.New("field \"timeout\" must be a number")
		}
		if !semanticTimeoutPattern.MatchString(decoded) {
			return 0, errors.New("field \"timeout\" must be a decimal number")
		}
		text = decoded
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, errors.New("field \"timeout\" must be a finite number")
	}
	return parsed, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("decode bash input: unexpected trailing JSON value")
	}
	return fmt.Errorf("decode bash input: %w", err)
}
