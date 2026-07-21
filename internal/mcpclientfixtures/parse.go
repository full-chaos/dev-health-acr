package mcpclientfixtures

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// StdioServerEntry is the structural shape of one "mcpServers" entry in
// the Claude Code and Cursor JSON-shaped client configs.
type StdioServerEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func ParseCommandArrayMCP(data []byte) (StdioServerEntry, error) {
	var doc struct {
		Schema string `json:"$schema"`
		MCP    map[string]struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
		} `json:"mcp"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return StdioServerEntry{}, fmt.Errorf("parse MCP command-array JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return StdioServerEntry{}, fmt.Errorf("parse MCP command-array JSON trailing data")
	}
	if len(doc.MCP) != 1 {
		return StdioServerEntry{}, fmt.Errorf("MCP command-array JSON must contain exactly one server")
	}
	if doc.Schema != "https://opencode.ai/config.json" {
		return StdioServerEntry{}, fmt.Errorf("MCP command-array JSON has invalid schema")
	}
	entry, ok := doc.MCP["acr"]
	if !ok || entry.Type != "local" || len(entry.Command) != 2 || entry.Command[0] != "acr-mcp" || entry.Command[1] != "serve" {
		return StdioServerEntry{}, fmt.Errorf("MCP command-array JSON must contain exact acr-mcp serve registration")
	}
	return StdioServerEntry{Type: entry.Type, Command: entry.Command[0], Args: entry.Command[1:]}, nil
}

// ParseStdioJSON parses a Claude Code or Cursor "mcpServers"-shaped JSON
// document (encoding/json handles the format; this just names the
// expected shape) and returns the "acr" entry, failing if it is absent.
func ParseStdioJSON(data []byte) (StdioServerEntry, error) {
	return parseStdioJSON(data, "cursor")
}

func ParseDocumentationStdioJSON(data []byte) (StdioServerEntry, error) {
	return parseStdioJSON(data, "documentation")
}

func ParseClaudeCodeJSON(data []byte) (StdioServerEntry, error) {
	return parseStdioJSON(data, "claude-code")
}

func ParseCodexJSON(data []byte) (StdioServerEntry, error) {
	return parseStdioJSON(data, "codex")
}

func parseStdioJSON(data []byte, client string) (StdioServerEntry, error) {
	var doc struct {
		MCPServers map[string]struct {
			Type                     string            `json:"type"`
			Command                  string            `json:"command"`
			Args                     []string          `json:"args"`
			Env                      map[string]string `json:"env"`
			Enabled                  *bool             `json:"enabled"`
			Required                 *bool             `json:"required"`
			DefaultToolsApprovalMode string            `json:"default_tools_approval_mode"`
			EnabledTools             []string          `json:"enabled_tools"`
		} `json:"mcpServers"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return StdioServerEntry{}, fmt.Errorf("parse mcpServers JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return StdioServerEntry{}, fmt.Errorf("parse mcpServers JSON trailing data")
	}
	if len(doc.MCPServers) != 1 {
		return StdioServerEntry{}, fmt.Errorf("mcpServers JSON must contain exactly one server")
	}
	entry, ok := doc.MCPServers["acr"]
	if !ok {
		return StdioServerEntry{}, fmt.Errorf("mcpServers JSON has no \"acr\" entry")
	}
	if client != "documentation" && (entry.Command != "acr-mcp" || len(entry.Args) != 1 || entry.Args[0] != "serve") {
		return StdioServerEntry{}, fmt.Errorf("mcpServers JSON must contain exact acr-mcp serve registration")
	}
	if client == "documentation" && (entry.Command == "" || len(entry.Args) != 1 || entry.Args[0] != "serve") {
		return StdioServerEntry{}, fmt.Errorf("documentation mcpServers JSON must contain a serve registration")
	}
	if client != "documentation" && len(entry.Env) != 0 {
		return StdioServerEntry{}, fmt.Errorf("mcpServers JSON must not inject environment")
	}
	switch client {
	case "cursor":
		if entry.Type != "stdio" || entry.Enabled != nil || entry.Required != nil || entry.DefaultToolsApprovalMode != "" || entry.EnabledTools != nil {
			return StdioServerEntry{}, fmt.Errorf("Cursor mcpServers JSON has invalid acr shape")
		}
	case "claude-code":
		if entry.Type != "" || entry.Enabled != nil || entry.Required != nil || entry.DefaultToolsApprovalMode != "" || entry.EnabledTools != nil {
			return StdioServerEntry{}, fmt.Errorf("Claude Code mcpServers JSON has invalid acr shape")
		}
	case "codex":
		if entry.Type != "" || entry.Enabled == nil || !*entry.Enabled || entry.Required == nil || *entry.Required || entry.DefaultToolsApprovalMode != "prompt" || !equalStrings(entry.EnabledTools, []string{"context_for_task", "source_evidence"}) {
			return StdioServerEntry{}, fmt.Errorf("Codex mcpServers JSON has invalid acr shape")
		}
	case "documentation":
		if entry.Type != "" && entry.Type != "stdio" {
			return StdioServerEntry{}, fmt.Errorf("documentation mcpServers JSON must use stdio when typed")
		}
	default:
		return StdioServerEntry{}, fmt.Errorf("unknown client parser %q", client)
	}
	return StdioServerEntry{Type: entry.Type, Command: entry.Command, Args: entry.Args, Env: entry.Env}, nil
}

// ParseLaunchScriptExec extracts the final `exec ...` invocation line from
// launch-sidecar.sh, without a general shell parser: it returns the exact
// trailing line so a caller can assert it targets exactly the sidecar
// binary variable and the "serve" subcommand.
func ParseLaunchScriptExec(data []byte) (string, error) {
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "exec ") {
			return line, nil
		}
	}
	return "", fmt.Errorf("no exec line found")
}
