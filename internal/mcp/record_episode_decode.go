package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func decodeRecordEpisodeRequest(arguments json.RawMessage) (contractsv1.MCPRecordEpisodeRequest, error) {
	if err := rejectDuplicateJSONFields(arguments); err != nil {
		return contractsv1.MCPRecordEpisodeRequest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	var input contractsv1.MCPRecordEpisodeRequest
	if err := decoder.Decode(&input); err != nil {
		return contractsv1.MCPRecordEpisodeRequest{}, fmt.Errorf("decode record_episode request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return contractsv1.MCPRecordEpisodeRequest{}, fmt.Errorf("decode record_episode request: trailing JSON")
	}
	if err := input.Validate(); err != nil {
		return contractsv1.MCPRecordEpisodeRequest{}, fmt.Errorf("validate record_episode request: %w", err)
	}
	return input, nil
}

func rejectDuplicateJSONFields(arguments json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read record_episode JSON: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return fmt.Errorf("record_episode JSON must be an object")
	}
	if err := consumeJSONObject(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("record_episode JSON has trailing content")
	}
	return nil
}

func consumeJSONObject(decoder *json.Decoder) error {
	seen := map[string]struct{}{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("read record_episode object key: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("record_episode object key is invalid")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("record_episode JSON has duplicate field %q", key)
		}
		seen[key] = struct{}{}
		if err := consumeJSONValue(decoder); err != nil {
			return err
		}
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("close record_episode object: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '}' {
		return fmt.Errorf("record_episode object is not closed")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read record_episode value: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return consumeJSONObject(decoder)
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("close record_episode array: %w", err)
		}
		if closingDelimiter, ok := closing.(json.Delim); !ok || closingDelimiter != ']' {
			return fmt.Errorf("record_episode array is not closed")
		}
		return nil
	default:
		return fmt.Errorf("record_episode JSON has unexpected delimiter")
	}
}
