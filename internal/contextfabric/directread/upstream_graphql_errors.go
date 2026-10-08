package directread

import (
	"encoding/json"
	"strings"
)

const (
	upstreamErrorMessageMaxRunes = 512
	upstreamErrorsMax            = 5
)

// upstreamGraphQLEntries reads the errors[] of a 200 GraphQL answer into
// bounded errors[] entries: message and path text only, at most
// upstreamErrorsMax entries of upstreamErrorMessageMaxRunes runes. The text is
// upstream text; the response marks errors as untrusted content.
func upstreamGraphQLEntries(raw []json.RawMessage) []OperationError {
	out := make([]OperationError, 0, min(len(raw), upstreamErrorsMax))
	for _, item := range raw {
		if len(out) == upstreamErrorsMax {
			break
		}
		var e struct {
			Message string            `json:"message"`
			Path    []json.RawMessage `json:"path"`
		}
		entry := OperationError{Class: UpstreamGraphQLErrors}
		if json.Unmarshal(item, &e) == nil {
			entry.Message = boundRunes(e.Message, upstreamErrorMessageMaxRunes)
			parts := make([]string, 0, len(e.Path))
			for _, seg := range e.Path {
				var s string
				if json.Unmarshal(seg, &s) != nil {
					s = strings.TrimSpace(string(seg))
				}
				parts = append(parts, s)
			}
			entry.Path = boundRunes(strings.Join(parts, "."), upstreamErrorMessageMaxRunes)
		}
		out = append(out, entry)
	}
	return out
}

func boundRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// logErrorMessage renders the first entry for the read log line.
func logErrorMessage(entries []OperationError) string {
	if len(entries) == 0 {
		return ""
	}
	first := entries[0]
	text := first.Message
	if first.Path != "" {
		text = first.Path + ": " + text
	}
	return boundRunes(text, upstreamErrorMessageMaxRunes)
}
