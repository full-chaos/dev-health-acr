package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func toolErrorLine(t *testing.T, callErr error) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	h := &HTTPHandler{cfg: &ProcessConfig{}, logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	rec := &requestRecord{}
	result := &mcpsdk.CallToolResult{}
	if callErr != nil {
		result.SetError(callErr)
	} else {
		result.IsError = true
	}
	rec.observe("tools/call", &mcpsdk.CallToolRequest{}, result, nil)
	h.emitRequestLine(context.Background(), requestLine{status: 200, authOutcome: HTTPAuthAdmitted}, rec, "", "")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("line: %v: %s", err, buf.String())
	}
	if line["result_class"] != HTTPResultToolError {
		t.Fatalf("result_class %v, want tool_error", line["result_class"])
	}
	return line
}

func TestToolErrorCategoryOnRequestLine(t *testing.T) {
	if got := toolErrorLine(t, &classifiedError{category: "no_data", message: "SECRET-MESSAGE"})["tool_error_category"]; got != "no_data" {
		t.Fatalf("classified: category %v, want no_data", got)
	}
	for name, err := range map[string]error{
		"no error":       nil,
		"plain error":    errors.New("SECRET-MESSAGE"),
		"wrapped plain":  fmt.Errorf("wrap: %w", errors.New("x")),
		"unknown":        &classifiedError{category: "not_in_set", message: "m"},
		"empty category": &classifiedError{message: "m"},
	} {
		line := toolErrorLine(t, err)
		if v, has := line["tool_error_category"]; has {
			t.Fatalf("%s: tool_error_category %v written, want left out", name, v)
		}
	}
}
