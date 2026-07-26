package websearch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ParseInput strictly decodes one complete WebSearch input object.
func ParseInput(data []byte) (Input, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return Input{}, fmt.Errorf("decode web search input: %w", err)
	}
	if fields == nil {
		return Input{}, errors.New("web search input must be an object")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Input{}, err
	}
	for name := range fields {
		switch name {
		case "query", "allowed_domains", "blocked_domains":
		default:
			return Input{}, fmt.Errorf("unknown field %q", name)
		}
	}

	query, err := decodeRequiredString("query", fields["query"])
	if err != nil {
		return Input{}, err
	}
	allowed, err := decodeOptionalStrings("allowed_domains", fields["allowed_domains"])
	if err != nil {
		return Input{}, err
	}
	blocked, err := decodeOptionalStrings("blocked_domains", fields["blocked_domains"])
	if err != nil {
		return Input{}, err
	}
	input := Input{Query: query, AllowedDomains: allowed, BlockedDomains: blocked}
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

func decodeOptionalStrings(name string, value json.RawMessage) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil, fmt.Errorf("field %q must be an array of strings", name)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(value, &values); err != nil {
		return nil, fmt.Errorf("field %q must be an array of strings: %w", name, err)
	}
	result := make([]string, len(values))
	for index, item := range values {
		if bytes.Equal(bytes.TrimSpace(item), []byte("null")) {
			return nil, fmt.Errorf("field %q item %d must be a string", name, index)
		}
		if err := json.Unmarshal(item, &result[index]); err != nil {
			return nil, fmt.Errorf("field %q item %d must be a string: %w", name, index, err)
		}
	}
	return result, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("decode web search input: unexpected trailing JSON value")
	}
	return fmt.Errorf("decode web search input: %w", err)
}
