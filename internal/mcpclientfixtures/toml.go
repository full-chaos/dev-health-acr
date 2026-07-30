package mcpclientfixtures

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CodexServerEntry is the structural shape this package's Codex TOML
// templates render: a [mcp_servers.acr] table plus its
// [mcp_servers.acr.env] sub-table. StartupTimeoutSec is nil when the
// Codex-only startup_timeout_sec field is absent (the primary
// codex-config.toml template omits it; only the "Full Configuration"
// narrative-doc variant sets it).
type CodexServerEntry struct {
	Command           string
	Args              []string
	Enabled           bool
	StartupTimeoutSec *float64
	Env               map[string]string
}

// ParseCodexTOML parses the narrow TOML subset codex-config.toml and its
// narrative-doc variants actually use: a `[mcp_servers.acr]` table and a
// `[mcp_servers.acr.env]` table, each containing only
// `key = "quoted string"`, `key = true/false`, `key = <number>`, or
// `key = ["quoted", "strings"]` assignments. This is deliberately not a
// general TOML parser -- these fixtures never need array-of-tables,
// inline tables, dotted keys, or multi-line strings -- so any line, key,
// or table outside that subset is rejected outright, any key redefined
// within the same table is rejected as a TOML spec violation, and the
// parsed result is checked for the fields this fixture family requires
// (command, args, and env.ACR_API_URL) before being returned. A future
// template change that outgrows this subset, or a hand-edit that drops a
// required field, fails the parity test loudly rather than being silently
// misparsed or silently accepted incomplete.
func ParseCodexTOML(data []byte) (CodexServerEntry, error) {
	entry := CodexServerEntry{Env: map[string]string{}}
	var section string
	seenTables := map[string]bool{}
	seenKeys := map[string]map[string]bool{}
	lineNumber := 0
	for rawLine := range strings.SplitSeq(string(data), "\n") {
		lineNumber++
		line := strings.TrimSpace(rawLine)
		switch {
		case line == "", strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if section != "mcp_servers.acr" && section != "mcp_servers.acr.env" {
				return CodexServerEntry{}, newConfigParseError(parserClassShape, "line %d: unrecognized table %q", lineNumber, section)
			}
			if seenTables[section] {
				return CodexServerEntry{}, newConfigParseError(parserClassShape, "line %d: duplicate table [%s]", lineNumber, section)
			}
			seenTables[section] = true
			if seenKeys[section] == nil {
				seenKeys[section] = map[string]bool{}
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return CodexServerEntry{}, newConfigParseError(parserClassSyntax, "line %d: expected a key = value assignment", lineNumber)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if seenKeys[section] == nil {
			return CodexServerEntry{}, newConfigParseError(parserClassShape, "line %d: assignment %q outside of any table", lineNumber, key)
		}
		if seenKeys[section][key] {
			return CodexServerEntry{}, newConfigParseError(parserClassShape, "line %d: duplicate key %q already defined earlier in [%s]", lineNumber, key, section)
		}
		seenKeys[section][key] = true
		if err := assignCodexField(&entry, section, key, value); err != nil {
			return CodexServerEntry{}, newConfigParseError(parserClassShape, "line %d: invalid TOML assignment", lineNumber)
		}
	}
	if !seenKeys["mcp_servers.acr"]["enabled"] {
		return CodexServerEntry{}, newConfigParseError(parserClassShape, "missing required key \"enabled\" in [mcp_servers.acr]")
	}
	if err := entry.validateRequiredFields(); err != nil {
		return CodexServerEntry{}, newConfigParseError(parserClassShape, "%v", err)
	}
	return entry, nil
}

// validateRequiredFields enforces the fields this fixture family actually
// requires for a working `acr-mcp serve` invocation: without any of
// these, ParseCodexTOML's caller would otherwise receive a silently
// incomplete entry rather than a loud parse failure.
func (e CodexServerEntry) validateRequiredFields() error {
	if e.Command == "" {
		return fmt.Errorf("missing required key \"command\" in [mcp_servers.acr]")
	}
	if len(e.Args) == 0 {
		return fmt.Errorf("missing required key \"args\" in [mcp_servers.acr]")
	}
	if !equalStrings(e.Args, []string{"serve"}) {
		return fmt.Errorf("args in [mcp_servers.acr] must be exactly [\"serve\"]")
	}
	if !e.Enabled {
		return fmt.Errorf("required key \"enabled\" in [mcp_servers.acr] must be true")
	}
	if e.Env["ACR_API_URL"] == "" {
		return fmt.Errorf("missing required key \"ACR_API_URL\" in [mcp_servers.acr.env]")
	}
	return nil
}

func assignCodexField(entry *CodexServerEntry, section, key, value string) error {
	switch section {
	case "mcp_servers.acr":
		return assignCodexServerField(entry, key, value)
	case "mcp_servers.acr.env":
		if key != "ACR_API_URL" && key != "ACR_API_TOKEN_FILE" && key != "ACR_API_TIMEOUT" {
			return fmt.Errorf("unrecognized key %q in [mcp_servers.acr.env]", key)
		}
		str, err := parseTOMLString(value)
		if err != nil {
			return err
		}
		entry.Env[key] = str
	default:
		return fmt.Errorf("unrecognized table %q", section)
	}
	return nil
}

func assignCodexServerField(entry *CodexServerEntry, key, value string) error {
	switch key {
	case "command":
		str, err := parseTOMLString(value)
		if err != nil {
			return err
		}
		entry.Command = str
	case "args":
		list, err := parseTOMLStringArray(value)
		if err != nil {
			return err
		}
		entry.Args = list
	case "enabled":
		if value != "true" {
			return fmt.Errorf("enabled must be the lowercase boolean true")
		}
		entry.Enabled = true
	case "startup_timeout_sec":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("parse number %q: %w", value, err)
		}
		entry.StartupTimeoutSec = &f
	default:
		return fmt.Errorf("unrecognized key %q in [mcp_servers.acr]", key)
	}
	return nil
}

// parseTOMLString decodes a TOML basic (double-quoted) string, including
// this fixture family's supported escape sequences: \", \\, \n, \t, \r,
// \b, \f, \uXXXX, and \UXXXXXXXX. codex-config.toml and its narrative-doc
// snippets only ever use basic strings -- never TOML literal ('...'),
// multi-line ("""..."""), or literal multi-line (”'...”') strings -- so
// only this one string form is supported; any other quoting, or an
// unsupported/incomplete escape sequence, is rejected rather than passed
// through unescaped or silently truncated.
func parseTOMLString(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("string must be valid UTF-8")
	}
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", fmt.Errorf("expected a quoted string")
	}
	inner := value[1 : len(value)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] != '\\' {
			if inner[i] == '"' || inner[i] < 0x20 {
				return "", fmt.Errorf("invalid unescaped character in string")
			}
			b.WriteByte(inner[i])
			continue
		}
		i++
		if i >= len(inner) {
			return "", fmt.Errorf("string ends with an incomplete escape sequence")
		}
		switch inner[i] {
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'u', 'U':
			digits := 4
			if inner[i] == 'U' {
				digits = 8
			}
			if i+digits >= len(inner) {
				return "", fmt.Errorf("incomplete unicode escape")
			}
			code, err := strconv.ParseInt(inner[i+1:i+1+digits], 16, 32)
			if err != nil {
				return "", fmt.Errorf("invalid unicode escape")
			}
			if code > utf8.MaxRune || code >= 0xD800 && code <= 0xDFFF {
				return "", fmt.Errorf("invalid unicode scalar value")
			}
			b.WriteRune(rune(code))
			i += digits
		default:
			return "", fmt.Errorf("unsupported escape sequence")
		}
	}
	return b.String(), nil
}

func parseTOMLStringArray(value string) ([]string, error) {
	if len(value) < 2 || value[0] != '[' || value[len(value)-1] != ']' {
		return nil, fmt.Errorf("expected a bracketed array, got %q", value)
	}
	inner := strings.TrimSpace(value[1 : len(value)-1])
	if inner == "" {
		return nil, nil
	}
	var result []string
	for part := range strings.SplitSeq(inner, ",") {
		str, err := parseTOMLString(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		result = append(result, str)
	}
	return result, nil
}
