package brief

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Status labels whether a message replies to the user or initiates contact.
type Status string

const (
	StatusNormal    Status = "normal"
	StatusProactive Status = "proactive"
)

// Input is the validated SendUserMessage input.
type Input struct {
	Message     string   `json:"message"`
	Attachments []string `json:"attachments,omitempty"`
	Status      Status   `json:"status"`
}

// ParseInput strictly decodes one complete SendUserMessage input object.
func ParseInput(data []byte) (Input, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))

	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return Input{}, fmt.Errorf("decode brief input: %w", err)
	}
	if fields == nil {
		return Input{}, errors.New("brief input must be an object")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Input{}, err
	}
	for name := range fields {
		switch name {
		case "message", "attachments", "status":
		default:
			return Input{}, fmt.Errorf("unknown field %q", name)
		}
	}

	message, err := decodeRequiredString("message", fields["message"])
	if err != nil {
		return Input{}, err
	}
	statusValue, err := decodeRequiredString("status", fields["status"])
	if err != nil {
		return Input{}, err
	}

	input := Input{Message: message, Status: Status(statusValue)}
	attachments := fields["attachments"]
	if attachments != nil {
		if bytes.Equal(bytes.TrimSpace(attachments), []byte("null")) {
			return Input{}, errors.New("attachments must be an array of strings")
		}
		var values []json.RawMessage
		if err := json.Unmarshal(attachments, &values); err != nil {
			return Input{}, fmt.Errorf("attachments must be an array of strings: %w", err)
		}
		input.Attachments = make([]string, len(values))
		for index, value := range values {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return Input{}, fmt.Errorf("attachment at index %d must be a string", index)
			}
			if err := json.Unmarshal(value, &input.Attachments[index]); err != nil {
				return Input{}, fmt.Errorf("attachment at index %d must be a string: %w", index, err)
			}
		}
	}
	if err := ValidateInput(input); err != nil {
		return Input{}, err
	}
	return input, nil
}

// ValidateInput validates typed inputs that did not pass through ParseInput.
func ValidateInput(input Input) error {
	switch input.Status {
	case StatusNormal, StatusProactive:
		return nil
	default:
		return fmt.Errorf("invalid status %q: must be %q or %q", input.Status, StatusNormal, StatusProactive)
	}
}

func decodeRequiredString(name string, value json.RawMessage) (string, error) {
	if value == nil {
		return "", fmt.Errorf("missing required field %q", name)
	}
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return "", fmt.Errorf("field %q must be a string", name)
	}
	var decoded string
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "", fmt.Errorf("field %q must be a string: %w", name, err)
	}
	return decoded, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("decode brief input: unexpected trailing JSON value")
	}
	return fmt.Errorf("decode brief input: %w", err)
}
