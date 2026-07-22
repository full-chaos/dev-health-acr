package mcpclientfixtures

import (
	"fmt"
	"strings"
)

const (
	parserClassSyntax = "syntax"
	parserClassShape  = "shape"
	parserClassPolicy = "policy"
)

// ConfigParseError classifies untrusted client configuration failures without
// returning client configuration content to callers.
type ConfigParseError struct {
	class string
	err   error
}

func (e *ConfigParseError) Error() string { return e.err.Error() }

func (e *ConfigParseError) Unwrap() error { return e.err }

func (e *ConfigParseError) Class() string { return e.class }

func newConfigParseError(class, format string, args ...any) error {
	return &ConfigParseError{class: class, err: fmt.Errorf(format, args...)}
}

// StdioServerEntry is the structural shape of one "mcpServers" entry in
// the Claude Code and Cursor JSON-shaped client configs.
type StdioServerEntry struct {
	Type    string
	Command string
	Args    []string
	Env     map[string]string
}

func ParseCommandArrayMCP(data []byte) (StdioServerEntry, error) {
	root, err := strictJSONObject(data)
	if err != nil {
		return StdioServerEntry{}, err
	}
	if err := requireKeys(root, "$schema", "mcp"); err != nil {
		return StdioServerEntry{}, err
	}
	schema, err := requiredJSONString(root, "$schema")
	if err != nil || schema != "https://opencode.ai/config.json" {
		return StdioServerEntry{}, newConfigParseError(parserClassShape, "MCP command-array JSON has invalid schema")
	}
	mcp, err := requiredJSONObject(root, "mcp")
	if err != nil || len(mcp) != 1 {
		return StdioServerEntry{}, newConfigParseError(parserClassShape, "MCP command-array JSON must contain exactly one server")
	}
	entryRaw, ok := mcp["acr"]
	if !ok {
		return StdioServerEntry{}, newConfigParseError(parserClassShape, "MCP command-array JSON has no acr server")
	}
	entry, err := rawJSONObject(entryRaw)
	if err != nil {
		return StdioServerEntry{}, err
	}
	if err := requireKeys(entry, "type", "command"); err != nil {
		return StdioServerEntry{}, err
	}
	typ, err := requiredJSONString(entry, "type")
	if err != nil || typ != "local" {
		return StdioServerEntry{}, newConfigParseError(parserClassShape, "MCP command-array JSON must use local type")
	}
	command, err := requiredJSONStringArray(entry, "command")
	if err != nil || !equalStrings(command, []string{"acr-mcp", "serve"}) {
		return StdioServerEntry{}, newConfigParseError(parserClassShape, "MCP command-array JSON must contain exact acr-mcp serve registration")
	}
	return StdioServerEntry{Type: typ, Command: command[0], Args: command[1:]}, nil
}

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
	root, err := strictJSONObject(data)
	if err != nil {
		return StdioServerEntry{}, err
	}
	if err := requireKeys(root, "mcpServers"); err != nil {
		return StdioServerEntry{}, err
	}
	servers, err := requiredJSONObject(root, "mcpServers")
	if err != nil || len(servers) != 1 {
		return StdioServerEntry{}, newConfigParseError(parserClassShape, "mcpServers JSON must contain exactly one server")
	}
	rawEntry, ok := servers["acr"]
	if !ok {
		return StdioServerEntry{}, newConfigParseError(parserClassShape, "mcpServers JSON has no acr entry")
	}
	entry, err := rawJSONObject(rawEntry)
	if err != nil {
		return StdioServerEntry{}, err
	}
	allowed, required := stdioFields(client)
	if allowed == nil {
		return StdioServerEntry{}, newConfigParseError(parserClassShape, "unknown client parser %q", client)
	}
	if err := requireKnownKeys(entry, allowed, required...); err != nil {
		return StdioServerEntry{}, err
	}
	command, err := requiredJSONString(entry, "command")
	if err != nil || command == "" {
		return StdioServerEntry{}, newConfigParseError(parserClassShape, "mcpServers JSON must contain a command")
	}
	args, err := requiredJSONStringArray(entry, "args")
	if err != nil || len(args) != 1 || args[0] != "serve" {
		return StdioServerEntry{}, newConfigParseError(parserClassShape, "mcpServers JSON must contain exact serve arguments")
	}
	if client != "documentation" && command != "acr-mcp" {
		return StdioServerEntry{}, newConfigParseError(parserClassShape, "mcpServers JSON must contain exact acr-mcp serve registration")
	}

	result := StdioServerEntry{Command: command, Args: args}
	switch client {
	case "cursor":
		result.Type, err = requiredJSONString(entry, "type")
		if err != nil || result.Type != "stdio" {
			return StdioServerEntry{}, newConfigParseError(parserClassShape, "Cursor mcpServers JSON has invalid acr shape")
		}
	case "claude-code":
	case "codex":
		if err := requireJSONBool(entry, "enabled", true); err != nil {
			return StdioServerEntry{}, err
		}
		if err := requireJSONBool(entry, "required", false); err != nil {
			return StdioServerEntry{}, err
		}
		approval, approvalErr := requiredJSONString(entry, "default_tools_approval_mode")
		tools, toolsErr := requiredJSONStringArray(entry, "enabled_tools")
		if approvalErr != nil || approval != "prompt" || toolsErr != nil || !equalStrings(tools, []string{"context_for_task", "source_evidence"}) {
			return StdioServerEntry{}, newConfigParseError(parserClassShape, "Codex mcpServers JSON has invalid acr shape")
		}
	case "documentation":
		if rawType, ok := entry["type"]; ok {
			result.Type, err = rawJSONString(rawType)
			if err != nil || result.Type != "stdio" {
				return StdioServerEntry{}, newConfigParseError(parserClassShape, "documentation mcpServers JSON must use stdio when typed")
			}
		}
		result.Env, err = optionalStringMap(entry, "env")
		if err != nil {
			return StdioServerEntry{}, err
		}
	}
	return result, nil
}

func stdioFields(client string) (map[string]bool, []string) {
	switch client {
	case "cursor":
		return keySet("type", "command", "args"), []string{"type", "command", "args"}
	case "claude-code":
		return keySet("command", "args"), []string{"command", "args"}
	case "codex":
		return keySet("command", "args", "enabled", "required", "default_tools_approval_mode", "enabled_tools"), []string{"command", "args", "enabled", "required", "default_tools_approval_mode", "enabled_tools"}
	case "documentation":
		return keySet("type", "command", "args", "env"), []string{"command", "args"}
	default:
		return nil, nil
	}
}

// ParseCodexPolicyYAML validates the exact structural policy fragment emitted
// for Codex without accepting a YAML implementation or permissive text match.
func ParseCodexPolicyYAML(data []byte) error {
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) != 5 {
		return newConfigParseError(parserClassSyntax, "Codex policy YAML has unsupported content")
	}
	expected := []string{
		"interface:",
		"  display_name: Context Fabric",
		"  short_description: Request evidence-backed context explicitly.",
		"policy:",
		"  allow_implicit_invocation: false",
	}
	for index, line := range expected {
		if lines[index] != line {
			class := parserClassShape
			if index == len(expected)-1 && strings.HasPrefix(lines[index], "  allow_implicit_invocation:") {
				class = parserClassPolicy
			}
			return newConfigParseError(class, "Codex policy YAML has invalid line %d", index+1)
		}
	}
	return nil
}

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
