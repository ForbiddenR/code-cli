package grep

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strconv"
)

const DefaultHeadLimit = 250

// OutputMode selects the Grep result representation.
type OutputMode string

const (
	OutputModeContent          OutputMode = "content"
	OutputModeFilesWithMatches OutputMode = "files_with_matches"
	OutputModeCount            OutputMode = "count"
)

// Input is the normalized GrepTool input.
type Input struct {
	Pattern         string     `json:"pattern"`
	Path            string     `json:"path,omitempty"`
	Glob            string     `json:"glob,omitempty"`
	OutputMode      OutputMode `json:"output_mode,omitempty"`
	Before          *int       `json:"-B,omitempty"`
	After           *int       `json:"-A,omitempty"`
	ContextAlias    *int       `json:"-C,omitempty"`
	Context         *int       `json:"context,omitempty"`
	ShowLineNumbers *bool      `json:"-n,omitempty"`
	CaseInsensitive *bool      `json:"-i,omitempty"`
	Type            string     `json:"type,omitempty"`
	HeadLimit       *int       `json:"head_limit,omitempty"`
	Offset          *int       `json:"offset,omitempty"`
	Multiline       *bool      `json:"multiline,omitempty"`
}

var semanticNumberPattern = regexp.MustCompile(`^-?\d+(?:\.\d+)?$`)

// ParseInput strictly decodes and normalizes one GrepTool input object.
func ParseInput(data []byte) (Input, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return Input{}, fmt.Errorf("decode grep input: %w", err)
	}
	if fields == nil {
		return Input{}, errors.New("grep input must be an object")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Input{}, err
	}
	for name := range fields {
		switch name {
		case "pattern", "path", "glob", "output_mode", "-B", "-A", "-C", "context", "-n", "-i", "type", "head_limit", "offset", "multiline":
		default:
			return Input{}, fmt.Errorf("unknown field %q", name)
		}
	}

	pattern, err := decodeRequiredString("pattern", fields["pattern"])
	if err != nil {
		return Input{}, err
	}
	input := Input{Pattern: pattern}
	for name, target := range map[string]*string{
		"path": &input.Path,
		"glob": &input.Glob,
		"type": &input.Type,
	} {
		if value := fields[name]; value != nil {
			if *target, err = decodeString(name, value); err != nil {
				return Input{}, err
			}
		}
	}
	if value := fields["output_mode"]; value != nil {
		mode, decodeErr := decodeString("output_mode", value)
		if decodeErr != nil {
			return Input{}, decodeErr
		}
		input.OutputMode = OutputMode(mode)
	}
	for name, target := range map[string]**int{
		"-B":         &input.Before,
		"-A":         &input.After,
		"-C":         &input.ContextAlias,
		"context":    &input.Context,
		"head_limit": &input.HeadLimit,
	} {
		if value := fields[name]; value != nil {
			parsed, decodeErr := decodeSemanticInt(name, value)
			if decodeErr != nil {
				return Input{}, decodeErr
			}
			*target = &parsed
		}
	}
	if value := fields["offset"]; value != nil {
		parsed, decodeErr := decodeSemanticInt("offset", value)
		if decodeErr != nil {
			return Input{}, decodeErr
		}
		input.Offset = &parsed
	}
	for name, target := range map[string]**bool{
		"-i":        &input.CaseInsensitive,
		"multiline": &input.Multiline,
	} {
		if value := fields[name]; value != nil {
			parsed, decodeErr := decodeSemanticBool(name, value)
			if decodeErr != nil {
				return Input{}, decodeErr
			}
			*target = &parsed
		}
	}
	if value := fields["-n"]; value != nil {
		parsed, decodeErr := decodeSemanticBool("-n", value)
		if decodeErr != nil {
			return Input{}, decodeErr
		}
		input.ShowLineNumbers = &parsed
	}
	if err := ValidateInput(input); err != nil {
		return Input{}, err
	}
	return input, nil
}

// ValidateInput validates typed values that did not pass through ParseInput.
func ValidateInput(input Input) error {
	switch input.OutputMode {
	case "", OutputModeContent, OutputModeFilesWithMatches, OutputModeCount:
	default:
		return fmt.Errorf("invalid output_mode %q", input.OutputMode)
	}
	for name, value := range map[string]*int{
		"-B":         input.Before,
		"-A":         input.After,
		"-C":         input.ContextAlias,
		"context":    input.Context,
		"head_limit": input.HeadLimit,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%s must be nonnegative", name)
		}
	}
	if input.Offset != nil && *input.Offset < 0 {
		return errors.New("offset must be nonnegative")
	}
	return nil
}

func (input Input) normalizedMode() OutputMode {
	if input.OutputMode == "" {
		return OutputModeFilesWithMatches
	}
	return input.OutputMode
}

func (input Input) lineNumbers() bool {
	return input.ShowLineNumbers == nil || *input.ShowLineNumbers
}

func (input Input) headLimit() int {
	if input.HeadLimit == nil {
		return DefaultHeadLimit
	}
	return *input.HeadLimit
}

func (input Input) offset() int {
	if input.Offset == nil {
		return 0
	}
	return *input.Offset
}

func (input Input) caseInsensitive() bool {
	return input.CaseInsensitive != nil && *input.CaseInsensitive
}

func (input Input) multiline() bool {
	return input.Multiline != nil && *input.Multiline
}

func (input Input) hasContext() bool {
	return input.Context != nil || input.ContextAlias != nil || input.Before != nil || input.After != nil
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

func decodeSemanticInt(name string, value json.RawMessage) (int, error) {
	trimmed := bytes.TrimSpace(value)
	if bytes.Equal(trimmed, []byte("null")) {
		return 0, fmt.Errorf("field %q must be a number", name)
	}
	text := string(trimmed)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var decoded string
		if err := json.Unmarshal(trimmed, &decoded); err != nil || !semanticNumberPattern.MatchString(decoded) {
			return 0, fmt.Errorf("field %q must be a number", name)
		}
		text = decoded
	}
	number, ok := new(big.Rat).SetString(text)
	if !ok || !number.IsInt() {
		return 0, fmt.Errorf("field %q must be a nonnegative integer", name)
	}
	integer := number.Num()
	if !integer.IsInt64() {
		return 0, fmt.Errorf("field %q is outside the supported integer range", name)
	}
	valueInt64 := integer.Int64()
	if strconv.IntSize == 32 && (valueInt64 > int64(^uint(0)>>1) || valueInt64 < -int64(^uint(0)>>1)-1) {
		return 0, fmt.Errorf("field %q is outside the supported integer range", name)
	}
	return int(valueInt64), nil
}

func decodeSemanticBool(name string, value json.RawMessage) (bool, error) {
	trimmed := bytes.TrimSpace(value)
	if bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte(`"true"`)) {
		return true, nil
	}
	if bytes.Equal(trimmed, []byte("false")) || bytes.Equal(trimmed, []byte(`"false"`)) {
		return false, nil
	}
	return false, fmt.Errorf("field %q must be a boolean", name)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("decode grep input: unexpected trailing JSON value")
	}
	return fmt.Errorf("decode grep input: %w", err)
}
