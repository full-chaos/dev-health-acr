package mcpclientfixtures

import (
	"encoding/json"
	"fmt"
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

// ParseStdioJSON parses a Claude Code or Cursor "mcpServers"-shaped JSON
// document (encoding/json handles the format; this just names the
// expected shape) and returns the "acr" entry, failing if it is absent.
func ParseStdioJSON(data []byte) (StdioServerEntry, error) {
	var doc struct {
		MCPServers map[string]StdioServerEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return StdioServerEntry{}, fmt.Errorf("parse mcpServers JSON: %w", err)
	}
	entry, ok := doc.MCPServers["acr"]
	if !ok {
		return StdioServerEntry{}, fmt.Errorf("mcpServers JSON has no \"acr\" entry")
	}
	return entry, nil
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
