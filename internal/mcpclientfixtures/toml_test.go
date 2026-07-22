package mcpclientfixtures

import (
	"errors"
	"strings"
	"testing"
)

const validCodexTOML = `[mcp_servers.acr]
command = "/path/to/acr-mcp"
args = ["serve"]
enabled = true

[mcp_servers.acr.env]
ACR_API_URL = "https://api.dev-health.example.com"
ACR_API_TOKEN_FILE = "/home/you/.acr/token"
`

// TestParseCodexTOMLRejectsDuplicateKey is the direct regression lock for
// the duplicate-key strengthening: TOML forbids redefining a key within
// the same table, and a hand-edited fixture that does so must be rejected
// rather than silently keeping whichever value the loop happened to see
// last.
func TestParseCodexTOMLRejectsDuplicateKey(t *testing.T) {
	doc := validCodexTOML + "\nACR_API_URL = \"https://second.example.com\"\n"
	if _, err := ParseCodexTOML([]byte(doc)); err == nil {
		t.Fatal("expected an error for a key redefined within [mcp_servers.acr.env]")
	} else if !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("expected a duplicate-key error, got: %v", err)
	}
}

func TestParseCodexTOMLRejectsDuplicateTable(t *testing.T) {
	// Given: a TOML document that reopens the ACR server table.
	doc := validCodexTOML + "\n[mcp_servers.acr]\ncommand = \"acr-mcp\"\nargs = [\"serve\"]\nenabled = true\n"
	// When: the structural parser consumes the document.
	_, err := ParseCodexTOML([]byte(doc))
	// Then: duplicate table headers cannot replace a validated registration.
	if err == nil || !strings.Contains(err.Error(), "duplicate table") {
		t.Fatalf("expected duplicate-table rejection, got: %v", err)
	}
}

func TestParseCodexTOMLRequiresExactNativeRegistration(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"missing enabled", strings.Replace(validCodexTOML, "enabled = true\n", "", 1)},
		{"uppercase enabled", strings.Replace(validCodexTOML, "enabled = true", "enabled = TRUE", 1)},
		{"false enabled", strings.Replace(validCodexTOML, "enabled = true", "enabled = false", 1)},
		{"extra server table", validCodexTOML + "\n[mcp_servers.other]\ncommand = \"other\"\nargs = [\"serve\"]\nenabled = true\n"},
		{"empty unknown table", validCodexTOML + "\n[unknown]\n"},
		{"case aliased table", strings.Replace(validCodexTOML, "[mcp_servers.acr]", "[MCP_SERVERS.acr]", 1)},
		{"arbitrary environment", validCodexTOML + "\nPATH = \"/tmp\"\n"},
		{"loader override", validCodexTOML + "\nloader = \"node\"\n"},
		{"invalid unicode", strings.Replace(validCodexTOML, `ACR_API_URL = "https://api.dev-health.example.com"`, `ACR_API_URL = "\uD800"`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a TOML configuration outside the exact supported client schema.
			// When: it is parsed as a Codex registration.
			_, err := ParseCodexTOML([]byte(tc.doc))
			// Then: it fails with a typed shape or syntax classification.
			if err == nil {
				t.Fatal("noncanonical TOML configuration was accepted")
			}
			var classified classifiedParserError
			if !errors.As(err, &classified) || (classified.Class() != "shape" && classified.Class() != "syntax") {
				t.Fatalf("expected typed TOML parser error, got %v", err)
			}
		})
	}
}

// TestParseCodexTOMLRejectsMissingRequiredFields covers each of the four
// fields this fixture family requires: dropping any one of them must fail
// closed instead of returning a silently incomplete entry.
func TestParseCodexTOMLRejectsMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"command", `[mcp_servers.acr]
args = ["serve"]
enabled = true

[mcp_servers.acr.env]
ACR_API_URL = "https://api.dev-health.example.com"
ACR_API_TOKEN_FILE = "/home/you/.acr/token"
`},
		{"args", `[mcp_servers.acr]
command = "/path/to/acr-mcp"
enabled = true

[mcp_servers.acr.env]
ACR_API_URL = "https://api.dev-health.example.com"
ACR_API_TOKEN_FILE = "/home/you/.acr/token"
`},
		{"ACR_API_URL", `[mcp_servers.acr]
command = "/path/to/acr-mcp"
args = ["serve"]
enabled = true

[mcp_servers.acr.env]
ACR_API_TOKEN_FILE = "/home/you/.acr/token"
`},
		{"ACR_API_TOKEN_FILE", `[mcp_servers.acr]
command = "/path/to/acr-mcp"
args = ["serve"]
enabled = true

[mcp_servers.acr.env]
ACR_API_URL = "https://api.dev-health.example.com"
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseCodexTOML([]byte(tc.doc)); err == nil {
				t.Fatalf("expected an error when %s is missing", tc.name)
			} else if !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("expected the error to name the missing field %q, got: %v", tc.name, err)
			}
		})
	}
}

// TestParseCodexTOMLDecodesEscapeSequences proves every escape sequence
// this fixture family supports is decoded correctly, not passed through
// with the backslashes still literal.
func TestParseCodexTOMLDecodesEscapeSequences(t *testing.T) {
	doc := `[mcp_servers.acr]
command = "/path/to/acr-mcp"
args = ["serve"]
enabled = true

[mcp_servers.acr.env]
ACR_API_URL = "https://api.dev-health.example.com"
ACR_API_TOKEN_FILE = "C:\\Users\\you\\.acr\\token"
	ACR_API_TIMEOUT = "quote:\" tab:\t newline:\n bell:\u0007 smiley:\U0001F600"
`
	entry, err := ParseCodexTOML([]byte(doc))
	if err != nil {
		t.Fatalf("ParseCodexTOML: %v", err)
	}
	if want := `C:\Users\you\.acr\token`; entry.Env["ACR_API_TOKEN_FILE"] != want {
		t.Fatalf("expected decoded path %q, got %q", want, entry.Env["ACR_API_TOKEN_FILE"])
	}
	if want := "quote:\" tab:\t newline:\n bell:\u0007 smiley:\U0001F600"; entry.Env["ACR_API_TIMEOUT"] != want {
		t.Fatalf("expected decoded escape string %q, got %q", want, entry.Env["ACR_API_TIMEOUT"])
	}
}

// TestParseCodexTOMLRejectsUnsupportedOrIncompleteEscape proves an
// unrecognized escape letter and a truncated escape both fail closed
// instead of silently dropping the backslash or reading out of bounds.
func TestParseCodexTOMLRejectsUnsupportedOrIncompleteEscape(t *testing.T) {
	base := `[mcp_servers.acr]
command = "/path/to/acr-mcp"
args = ["serve"]
enabled = true

[mcp_servers.acr.env]
ACR_API_URL = "https://api.dev-health.example.com"
ACR_API_TOKEN_FILE = "/home/you/.acr/token"
BAD = %s
`
	cases := []string{`"\q"`, `"\`, `"\u12"`}
	for _, value := range cases {
		if _, err := ParseCodexTOML([]byte(strings.Replace(base, "%s", value, 1))); err == nil {
			t.Fatalf("expected an error for malformed escape value %s", value)
		}
	}
}

func TestParseCodexTOMLRejectsMalformedTextWithoutLeakingValues(t *testing.T) {
	base := validCodexTOML + "\nACR_API_TIMEOUT = %s\n"
	cases := []struct {
		name  string
		value string
	}{
		{"unescaped quote", `"bad"tail"`},
		{"control byte", "\"bad\x01text\""},
		{"invalid utf8", string([]byte{'"', 0xff, '"'})},
		{"secret malformed escape", `"\qTOP_SECRET"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: malformed text carrying a value that must never be reflected.
			doc := strings.Replace(base, "%s", tc.value, 1)
			// When: the narrow TOML parser validates it.
			_, err := ParseCodexTOML([]byte(doc))
			// Then: parsing fails without returning the malformed value.
			if err == nil {
				t.Fatal("malformed TOML text was accepted")
			}
			if strings.Contains(err.Error(), "TOP_SECRET") {
				t.Fatalf("parser error leaked secret-bearing input: %v", err)
			}
		})
	}
}

// TestParseCodexTOMLParsesStartupTimeoutSecVariant proves the parser
// handles codex.md's "Full Configuration" variant -- the one snippet that
// sets the Codex-only startup_timeout_sec field -- including decoding it
// as a float rather than rejecting it as an unrecognized key.
func TestParseCodexTOMLParsesStartupTimeoutSecVariant(t *testing.T) {
	entry, err := ParseCodexTOML([]byte(RenderCodexTOMLFullExample()))
	if err != nil {
		t.Fatalf("ParseCodexTOML(RenderCodexTOMLFullExample()): %v", err)
	}
	if entry.StartupTimeoutSec == nil || *entry.StartupTimeoutSec != 10.0 {
		t.Fatalf("expected StartupTimeoutSec 10.0, got %v", entry.StartupTimeoutSec)
	}
	if entry.Env["ACR_API_TIMEOUT"] != "60s" {
		t.Fatalf("expected ACR_API_TIMEOUT 60s, got %q", entry.Env["ACR_API_TIMEOUT"])
	}
}

// TestParseCodexTOMLDocSnippetVariantParses proves the reduced doc-snippet
// variant (no header comment, no ACR_API_TIMEOUT) still parses to valid,
// complete required fields.
func TestParseCodexTOMLDocSnippetVariantParses(t *testing.T) {
	entry, err := ParseCodexTOML([]byte(RenderCodexTOMLDocSnippet()))
	if err != nil {
		t.Fatalf("ParseCodexTOML(RenderCodexTOMLDocSnippet()): %v", err)
	}
	if entry.Command != ExampleCommand || len(entry.Args) != 1 || entry.Args[0] != ServeArg {
		t.Fatalf("unexpected entry: %#v", entry)
	}
	if _, ok := entry.Env["ACR_API_TIMEOUT"]; ok {
		t.Fatal("expected no ACR_API_TIMEOUT in the reduced doc-snippet variant")
	}
}
