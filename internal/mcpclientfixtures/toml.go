package mcpclientfixtures

import (
	"fmt"
	"strconv"
	"strings"
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
// (command, args, and the env.ACR_API_URL/ACR_API_TOKEN_FILE pair) before
// being returned. A future template change that outgrows this subset, or
// a hand-edit that drops a required field, fails the parity test loudly
// rather than being silently misparsed or silently accepted incomplete.
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
			if seenTables[section] {
				return CodexServerEntry{}, fmt.Errorf("line %d: duplicate table [%s]", lineNumber+1, section)
			}
			seenTables[section] = true
			if seenKeys[section] == nil {
				seenKeys[section] = map[string]bool{}
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return CodexServerEntry{}, fmt.Errorf("line %d: expected a \"key = value\" assignment, got %q", lineNumber+1, rawLine)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if seenKeys[section] == nil {
			return CodexServerEntry{}, fmt.Errorf("line %d: assignment %q outside of any [table]", lineNumber+1, key)
		}
		if seenKeys[section][key] {
			return CodexServerEntry{}, fmt.Errorf("line %d: duplicate key %q already defined earlier in [%s]", lineNumber+1, key, section)
		}
		seenKeys[section][key] = true
		if err := assignCodexField(&entry, section, key, value); err != nil {
			return CodexServerEntry{}, fmt.Errorf("line %d: %w", lineNumber+1, err)
		}
	}
	if err := entry.validateRequiredFields(); err != nil {
		return CodexServerEntry{}, err
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
	if e.Env["ACR_API_URL"] == "" {
		return fmt.Errorf("missing required key \"ACR_API_URL\" in [mcp_servers.acr.env]")
	}
	if e.Env["ACR_API_TOKEN_FILE"] == "" {
		return fmt.Errorf("missing required key \"ACR_API_TOKEN_FILE\" in [mcp_servers.acr.env]")
	}
	return nil
}

func assignCodexField(entry *CodexServerEntry, section, key, value string) error {
	switch section {
	case "mcp_servers.acr":
		return assignCodexServerField(entry, key, value)
	case "mcp_servers.acr.env":
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
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse boolean %q: %w", value, err)
		}
		entry.Enabled = b
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
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", fmt.Errorf("expected a quoted string, got %q", value)
	}
	inner := value[1 : len(value)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] != '\\' {
			b.WriteByte(inner[i])
			continue
		}
		i++
		if i >= len(inner) {
			return "", fmt.Errorf("string ends with an incomplete escape sequence: %q", value)
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
				return "", fmt.Errorf("incomplete unicode escape in %q", value)
			}
			code, err := strconv.ParseInt(inner[i+1:i+1+digits], 16, 32)
			if err != nil {
				return "", fmt.Errorf("invalid unicode escape in %q: %w", value, err)
			}
			b.WriteRune(rune(code))
			i += digits
		default:
			return "", fmt.Errorf("unsupported escape sequence \\%c in %q", inner[i], value)
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
