package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// decodeToolArguments is the one request decode of every MCP tool: a key the
// request type does not declare, at any depth, is refused rather than dropped,
// matching the wire schemas' additionalProperties: false. A misplaced field
// (a budget field beside the question, say) would otherwise be ignored and the
// default served. Exactly one JSON value is accepted; anything after it,
// including a closing delimiter, is refused. useNumber keeps numbers exact for
// the tools that forward them.
func decodeToolArguments(args []byte, into any, useNumber bool) error {
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if useNumber {
		decoder.UseNumber()
	}
	if err := decoder.Decode(into); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

const jsonUnknownFieldPrefix = "json: unknown field "

// toolArgumentsMessage is the validation message of a refused decode. An
// undeclared key is named (the caller's own key, quoted and bounded to 64
// characters); any other decode failure keeps the generic text.
func toolArgumentsMessage(tool string, err error) string {
	if text := err.Error(); strings.HasPrefix(text, jsonUnknownFieldPrefix) {
		name := strings.Trim(strings.TrimPrefix(text, jsonUnknownFieldPrefix), `"`)
		if len(name) > 64 {
			name = name[:64]
		}
		return tool + " arguments carry a field the schema does not declare: " + strconv.Quote(name)
	}
	return tool + " arguments are not valid JSON for the declared schema"
}
