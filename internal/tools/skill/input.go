package skill

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Input is the model-facing Skill invocation.
type Input struct {
	Skill string  `json:"skill"`
	Args  *string `json:"args,omitempty"`
}

// ParseInput strictly decodes one Skill input object.
func ParseInput(data []byte) (Input, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return Input{}, fmt.Errorf("decode input: %w", err)
	}
	if fields == nil {
		return Input{}, errors.New("input must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Input{}, errors.New("input must contain exactly one JSON value")
		}
		return Input{}, fmt.Errorf("decode trailing input: %w", err)
	}
	for name := range fields {
		if name != "skill" && name != "args" {
			return Input{}, fmt.Errorf("unknown field %q", name)
		}
	}
	rawSkill, ok := fields["skill"]
	if !ok {
		return Input{}, errors.New("skill is required")
	}
	var input Input
	if bytes.Equal(bytes.TrimSpace(rawSkill), []byte("null")) {
		return Input{}, errors.New("skill must be a string")
	}
	if err := json.Unmarshal(rawSkill, &input.Skill); err != nil {
		return Input{}, errors.New("skill must be a string")
	}
	if rawArgs, ok := fields["args"]; ok {
		if bytes.Equal(bytes.TrimSpace(rawArgs), []byte("null")) {
			return Input{}, errors.New("args must be a string")
		}
		var args string
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return Input{}, errors.New("args must be a string")
		}
		input.Args = &args
	}
	if err := ValidateInput(&input); err != nil {
		return Input{}, err
	}
	return input, nil
}

// ValidateInput normalizes and validates a typed Skill input.
func ValidateInput(input *Input) error {
	if input == nil {
		return errors.New("input is nil")
	}
	name := strings.TrimSpace(input.Skill)
	if after, ok := strings.CutPrefix(name, "/"); ok {
		name = after
	}
	if name == "" || name == "." || name == ".." {
		return errors.New("skill name is empty or invalid")
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return errors.New("skill name must not contain path separators or NUL")
	}
	input.Skill = name
	return nil
}
