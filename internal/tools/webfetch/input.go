package webfetch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ParseInput strictly decodes one complete WebFetch input object.
func ParseInput(data []byte) (Input, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return Input{}, fmt.Errorf("decode web fetch input: %w", err)
	}
	if fields == nil {
		return Input{}, errors.New("web fetch input must be an object")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Input{}, err
	}
	for name := range fields {
		switch name {
		case "url", "prompt":
		default:
			return Input{}, fmt.Errorf("unknown field %q", name)
		}
	}

	url, err := decodeRequiredString("url", fields["url"])
	if err != nil {
		return Input{}, err
	}
	prompt, err := decodeRequiredString("prompt", fields["prompt"])
	if err != nil {
		return Input{}, err
	}
	input := Input{URL: url, Prompt: prompt}
	if validation := ValidateInput(input); !validation.Valid {
		return Input{}, errors.New(validation.Message)
	}
	return input, nil
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
		return errors.New("decode web fetch input: unexpected trailing JSON value")
	}
	return fmt.Errorf("decode web fetch input: %w", err)
}
