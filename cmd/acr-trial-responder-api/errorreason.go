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
// so the transport is the one place the reason is readable. Only the four
// fields of the provider's error object are reported (type, code, param,
// message), each flattened to one bounded line; the request, the schema and
// the rest of the body are never read into the report.
type reasonLogTransport struct {
	next   http.RoundTripper
	report func(reason string)
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
		t.report(providerErrorReason(resp.StatusCode, raw))
	}
	return resp, nil
}

func providerErrorReason(status int, body []byte) string {
	var envelope struct {
		Error struct {
			Type    any    `json:"type"`
			Code    any    `json:"code"`
			Param   any    `json:"param"`
			Message string `json:"message"`
		} `json:"error"`
	}
	field := func(v any) string {
		s, _ := v.(string)
		return oneLine(s, 80)
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "http=" + strconv.Itoa(status) + " body_not_json"
	}
	e := envelope.Error
	return "http=" + strconv.Itoa(status) + " type=" + field(e.Type) + " code=" + field(e.Code) + " param=" + field(e.Param) + " message=" + oneLine(e.Message, 400)
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		s = s[:limit] + "..."
	}
	return s
}
