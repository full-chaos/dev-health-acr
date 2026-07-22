package nativeadapters

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func Parse(client Client, output []byte) error {
	switch client {
	case OpenCode:
		return parseOpenCode(output)
	case Claude:
		return parseClaude(output)
	case Codex:
		return parseCodex(output)
	case Cursor:
		return parseCursor(output)
	default:
		return fmt.Errorf("native adapter: unsupported client %q", client)
	}
}

func records(output []byte) ([]map[string]json.RawMessage, error) {
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	if len(lines) == 0 || len(lines) > 3 || len(lines[0]) == 0 {
		return nil, fmt.Errorf("native adapter: record count")
	}
	parsed := make([]map[string]json.RawMessage, 0, len(lines))
	for _, line := range lines {
		var record map[string]json.RawMessage
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("native adapter: malformed jsonl: %w", err)
		}
		parsed = append(parsed, record)
	}
	return parsed, nil
}

func exact(record map[string]json.RawMessage, expected map[string]string) error {
	if len(record) != len(expected) {
		return fmt.Errorf("native adapter: unexpected fields")
	}
	for key, value := range expected {
		raw, ok := record[key]
		if !ok || string(raw) != value {
			return fmt.Errorf("native adapter: invalid %s", key)
		}
	}
	return nil
}

func parseOpenCode(output []byte) error {
	record, err := records(output)
	if err != nil || len(record) != 3 {
		return fmt.Errorf("opencode jsonl: %w", err)
	}
	for index, expected := range []map[string]string{{"type": `"mcp.connected"`, "server": `"acr"`}, {"type": `"mcp.tool"`, "name": `"context_for_task"`}, {"type": `"result"`, "status": `"ok"`}} {
		if err := exact(record[index], expected); err != nil {
			return fmt.Errorf("opencode jsonl: %w", err)
		}
	}
	return nil
}

func parseClaude(output []byte) error {
	record, err := records(output)
	if err != nil || len(record) != 3 {
		return fmt.Errorf("claude stream-json: %w", err)
	}
	for index, expected := range []map[string]string{{"type": `"system"`, "subtype": `"init"`}, {"type": `"assistant"`, "event": `"tool_use"`, "tool": `"context_for_task"`}, {"type": `"result"`, "subtype": `"success"`}} {
		if err := exact(record[index], expected); err != nil {
			return fmt.Errorf("claude stream-json: %w", err)
		}
	}
	return nil
}

func parseCodex(output []byte) error {
	record, err := records(output)
	if err != nil || len(record) != 2 {
		return fmt.Errorf("codex jsonl: %w", err)
	}
	for index, expected := range []map[string]string{{"type": `"mcp_tool_call"`, "tool": `"context_for_task"`}, {"type": `"completion"`, "status": `"completed"`}} {
		if err := exact(record[index], expected); err != nil {
			return fmt.Errorf("codex jsonl: %w", err)
		}
	}
	return nil
}

func parseCursor(output []byte) error {
	record, err := records(output)
	if err != nil || len(record) != 2 {
		return fmt.Errorf("cursor json: %w", err)
	}
	for index, expected := range []map[string]string{{"type": `"mcp"`, "server": `"acr"`}, {"type": `"result"`, "status": `"ok"`}} {
		if err := exact(record[index], expected); err != nil {
			return fmt.Errorf("cursor json: %w", err)
		}
	}
	return nil
}
