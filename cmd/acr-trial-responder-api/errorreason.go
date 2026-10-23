package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const providerErrorBodyLimit = 16 << 10

// reasonLogTransport reports the reason a provider gave for a non-2xx
// response. The shared llm client keeps the provider body out of its errors,
// so the transport is the one place the reason is readable. Only the
// provider's error type, code and param are reported, each flattened to one
// bounded line, plus the message only on explicit opt-in; the request, the schema and
// the rest of the body are never read into the report.
type reasonLogTransport struct {
	next   http.RoundTripper
	report func(reason string)
	// includeMessage is an explicit operator opt-in: the free-text message is
	// provider-controlled and a gateway may echo request content into it, so
	// it is off by default and only its length is reported.
	includeMessage bool
}

func (t reasonLogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp == nil || resp.StatusCode < 400 || resp.Body == nil {
		return resp, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, providerErrorBodyLimit))
	rest := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(raw), rest), rest}
	if readErr == nil {
		t.report(providerErrorReason(resp.StatusCode, raw, t.includeMessage))
	}
	return resp, nil
}

func providerErrorReason(status int, body []byte, includeMessage bool) string {
	var envelope struct {
		Error struct {
			Type    any    `json:"type"`
			Code    any    `json:"code"`
			Param   any    `json:"param"`
			Message string `json:"message"`
		} `json:"error"`
	}
	field := func(v any, known map[string]bool) string {
		s, _ := v.(string)
		if s == "" || known[s] {
			return s
		}
		return "other"
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "http=" + strconv.Itoa(status) + " body_not_json"
	}
	e := envelope.Error
	return "http=" + strconv.Itoa(status) + " type=" + field(e.Type, knownProviderErrorTypes) + " code=" + field(e.Code, knownProviderErrorCodes) + " param=" + field(e.Param, knownProviderErrorParams) + messagePart(e.Message, includeMessage)
}

func messagePart(message string, include bool) string {
	if !include {
		return " message_bytes=" + strconv.Itoa(len(message))
	}
	return " message=" + oneLine(message, 400)
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		s = s[:limit] + "..."
	}
	return s
}

// type, code and param are provider-controlled strings, so only members of
// these closed sets are reported; anything else is reported as "other".
var (
	knownProviderErrorTypes = map[string]bool{
		"invalid_request_error": true, "authentication_error": true, "permission_error": true,
		"rate_limit_error": true, "server_error": true, "not_found_error": true, "insufficient_quota": true,
	}
	knownProviderErrorCodes = map[string]bool{
		"invalid_json_schema": true, "invalid_value": true, "unsupported_value": true, "unsupported_parameter": true,
		"context_length_exceeded": true, "invalid_api_key": true, "model_not_found": true,
		"rate_limit_exceeded": true, "insufficient_quota": true, "invalid_type": true, "missing_required_parameter": true,
	}
	knownProviderErrorParams = map[string]bool{
		"response_format": true, "messages": true, "model": true, "reasoning_effort": true,
		"temperature": true, "max_tokens": true, "max_completion_tokens": true, "seed": true,
	}
)
