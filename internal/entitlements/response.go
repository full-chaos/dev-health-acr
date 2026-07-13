package entitlements

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

type response struct {
	SchemaVersion       string
	OrgID               string
	AgentContextRuntime bool
}

type healthResponse struct {
	SchemaVersion string
	Service       string
	Status        string
}

func decodeResponse(body io.Reader, maxBytes int64) (response, error) {
	payload, err := readBounded(body, maxBytes)
	if err != nil {
		return response{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return response{}, errors.New("invalid response object")
	}
	parsed, seen, err := decodeFields(decoder)
	if err != nil || !seen.schema || !seen.org || !seen.entitlement {
		return response{}, errors.New("invalid entitlement response")
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return response{}, errors.New("invalid response object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return response{}, errors.New("trailing JSON")
	}
	return parsed, nil
}

type responseFields struct{ schema, org, entitlement bool }

func decodeFields(decoder *json.Decoder) (response, responseFields, error) {
	var parsed response
	var seen responseFields
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return response{}, responseFields{}, err
		}
		switch name {
		case "schema_version":
			if seen.schema || decodeRequiredValue(decoder, &parsed.SchemaVersion) != nil {
				return response{}, responseFields{}, errors.New("invalid schema_version")
			}
			seen.schema = true
		case "org_id":
			if seen.org || decodeRequiredValue(decoder, &parsed.OrgID) != nil {
				return response{}, responseFields{}, errors.New("invalid org_id")
			}
			seen.org = true
		case "agent_context_runtime":
			if seen.entitlement || decodeRequiredValue(decoder, &parsed.AgentContextRuntime) != nil {
				return response{}, responseFields{}, errors.New("invalid agent_context_runtime")
			}
			seen.entitlement = true
		default:
			return response{}, responseFields{}, errors.New("unknown response field")
		}
	}
	return parsed, seen, nil
}

func decodeHealthResponse(body io.Reader, maxBytes int64) (healthResponse, error) {
	payload, err := readBounded(body, maxBytes)
	if err != nil {
		return healthResponse{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return healthResponse{}, errors.New("invalid response object")
	}
	var parsed healthResponse
	var seen struct{ schema, service, status bool }
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return healthResponse{}, err
		}
		switch name {
		case "schema_version":
			if seen.schema || decodeRequiredValue(decoder, &parsed.SchemaVersion) != nil {
				return healthResponse{}, errors.New("invalid schema_version")
			}
			seen.schema = true
		case "service":
			if seen.service || decodeRequiredValue(decoder, &parsed.Service) != nil {
				return healthResponse{}, errors.New("invalid service")
			}
			seen.service = true
		case "status":
			if seen.status || decodeRequiredValue(decoder, &parsed.Status) != nil {
				return healthResponse{}, errors.New("invalid status")
			}
			seen.status = true
		default:
			return healthResponse{}, errors.New("unknown response field")
		}
	}
	if !seen.schema || !seen.service || !seen.status {
		return healthResponse{}, errors.New("invalid health response")
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return healthResponse{}, errors.New("invalid response object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return healthResponse{}, errors.New("trailing JSON")
	}
	return parsed, nil
}

func decodeRequiredValue(decoder *json.Decoder, target any) error {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil || bytes.Equal(raw, []byte("null")) {
		return errors.New("null or malformed response field")
	}
	return json.Unmarshal(raw, target)
}

func readBounded(body io.Reader, maxBytes int64) ([]byte, error) {
	limited := &io.LimitedReader{R: body, N: maxBytes + 1}
	payload, err := io.ReadAll(limited)
	if err != nil || int64(len(payload)) > maxBytes {
		return nil, errors.New("response exceeds limit")
	}
	return payload, nil
}
