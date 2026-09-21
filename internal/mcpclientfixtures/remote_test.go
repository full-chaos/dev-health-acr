package mcpclientfixtures

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var remoteFixtureFiles = []struct {
	client RemoteClient
	path   string
}{
	{RemoteClaudeCode, "docs/examples/mcp-clients/claude-code-remote-mcp.json"},
	{RemoteCodex, "docs/examples/mcp-clients/codex-remote-config.toml"},
	{RemoteCursor, "docs/examples/mcp-clients/cursor-remote-mcp-config.json"},
	{RemoteOpenCode, "docs/examples/mcp-clients/opencode-remote-config.json"},
}

// TestRemoteFixturesMatchCanonicalModel: every checked-in remote config is
// byte-for-byte what the canonical model renders, and the file list covers
// every RemoteClients member exactly once.
func TestRemoteFixturesMatchCanonicalModel(t *testing.T) {
	root := findRepoRoot(t)
	if len(remoteFixtureFiles) != len(RemoteClients) {
		t.Fatalf("fixture table has %d rows, RemoteClients has %d", len(remoteFixtureFiles), len(RemoteClients))
	}
	for _, tc := range remoteFixtureFiles {
		t.Run(tc.path, func(t *testing.T) {
			want, ok := RenderRemote(tc.client)
			if !ok {
				t.Fatalf("no renderer for %s", tc.client)
			}
			got, err := os.ReadFile(filepath.Join(root, tc.path))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Fatalf("%s drifted from the canonical model.\n--- file ---\n%s\n--- render ---\n%s", tc.path, got, want)
			}
		})
	}
}

// TestRemoteGoldenShapes pins the exact per-client shape from the vendor docs:
// key names, the URL, and the client-specific env expansion spelling.
func TestRemoteGoldenShapes(t *testing.T) {
	type entry struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Enabled *bool             `json:"enabled"`
		Headers map[string]string `json:"headers"`
	}
	decode := func(t *testing.T, raw, top string) entry {
		t.Helper()
		var doc map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, raw)
		}
		var servers map[string]entry
		if err := json.Unmarshal(doc[top], &servers); err != nil {
			t.Fatalf("no %q object: %v", top, err)
		}
		e, ok := servers[RemoteServerName]
		if !ok || len(servers) != 1 {
			t.Fatalf("want exactly one %q entry under %q, got %v", RemoteServerName, top, servers)
		}
		return e
	}
	cases := []struct {
		name    string
		raw     string
		top     string
		typ     string
		enabled bool
		auth    string
	}{
		{"claude-code", RenderClaudeCodeRemoteJSON(), "mcpServers", "http", false, "Bearer ${ACR_MCP_TOKEN}"},
		{"cursor", RenderCursorRemoteJSON(), "mcpServers", "", false, "Bearer ${env:ACR_MCP_TOKEN}"},
		{"opencode", RenderOpenCodeRemoteJSON(), "mcp", "remote", true, "Bearer {env:ACR_MCP_TOKEN}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := decode(t, tc.raw, tc.top)
			if e.URL != ExampleRemoteURL || e.Type != tc.typ {
				t.Fatalf("url/type = %q/%q", e.URL, e.Type)
			}
			if (e.Enabled != nil) != tc.enabled {
				t.Fatalf("enabled key presence = %v, want %v", e.Enabled != nil, tc.enabled)
			}
			if len(e.Headers) != 1 || e.Headers["Authorization"] != tc.auth {
				t.Fatalf("headers = %v, want only Authorization=%q", e.Headers, tc.auth)
			}
		})
	}

	const wantTOML = `# Example ACR MCP remote (hosted) server entry for Codex CLI.
# Codex sends the value of the named environment variable as
# "Authorization: Bearer <value>" on every request. Export the variable in the
# shell that starts Codex; never write the token into this file. Place this
# table in ~/.codex/config.toml (user scope) or .codex/config.toml (project
# scope, requires trusting the project on first use).

[mcp_servers.acr]
url = "https://acr-mcp.dev-health.example.com/mcp"
bearer_token_env_var = "ACR_MCP_TOKEN"
enabled = true
`
	if got := RenderCodexRemoteTOML(); got != wantTOML {
		t.Fatalf("codex remote TOML golden mismatch:\n%s", got)
	}
	const wantAdd = `claude mcp add --transport http acr https://acr-mcp.dev-health.example.com/mcp --header 'Authorization: Bearer ${ACR_MCP_TOKEN}'`
	if got := RenderClaudeCodeRemoteAddCommand(); got != wantAdd {
		t.Fatalf("claude mcp add golden mismatch: %s", got)
	}
}

// TestRemoteFixturesNeverEmbedALiteralSecret: every rendered remote config
// names the token only through the client's env expansion, never a literal.
// The token shape check is independent of the placeholder name.
func TestRemoteFixturesNeverEmbedALiteralSecret(t *testing.T) {
	token := regexp.MustCompile(`fcacr_[A-Za-z0-9_-]+|[A-Za-z0-9_-]{40,}`)
	expansion := map[RemoteClient]string{
		RemoteClaudeCode: "${" + RemoteTokenEnvVar + "}",
		RemoteCodex:      "", // bearer_token_env_var carries the bare variable name
		RemoteCursor:     "${env:" + RemoteTokenEnvVar + "}",
		RemoteOpenCode:   "{env:" + RemoteTokenEnvVar + "}",
	}
	for _, client := range RemoteClients {
		rendered, _ := RenderRemote(client)
		if token.MatchString(rendered) {
			t.Errorf("%s: rendered config contains a token-shaped literal:\n%s", client, rendered)
		}
		if !strings.Contains(rendered, RemoteTokenEnvVar) {
			t.Errorf("%s: config does not reference %s", client, RemoteTokenEnvVar)
		}
		if want := expansion[client]; want != "" && !strings.Contains(rendered, want) {
			t.Errorf("%s: config lacks the client expansion %q", client, want)
		}
	}
	if add := RenderClaudeCodeRemoteAddCommand(); !strings.Contains(add, "'Authorization: Bearer ${"+RemoteTokenEnvVar+"}'") {
		t.Errorf("claude mcp add must single-quote the header so the shell cannot expand the token: %s", add)
	}
	// Control: the detector fires on a token-shaped value.
	if !token.MatchString(`"Authorization": "Bearer fcacr_` + strings.Repeat("a", 43) + `"`) {
		t.Fatal("secret detector does not match a token-shaped literal")
	}
}

// TestRemoteFixturesUseHTTPSAndBasePath: the URL is https and ends in the
// server's default base path; the STDIO-only keys never appear.
func TestRemoteFixturesUseHTTPSAndBasePath(t *testing.T) {
	if !strings.HasPrefix(ExampleRemoteURL, "https://") || !strings.HasSuffix(ExampleRemoteURL, "/mcp") {
		t.Fatalf("ExampleRemoteURL = %q", ExampleRemoteURL)
	}
	for _, client := range RemoteClients {
		rendered, _ := RenderRemote(client)
		for _, stdioOnly := range []string{`"command"`, `command =`, `"args"`, `ACR_API_URL`, `ACR_API_TOKEN`} {
			if strings.Contains(rendered, stdioOnly) {
				t.Errorf("%s: remote config contains STDIO-only %q", client, stdioOnly)
			}
		}
	}
	if _, ok := RenderRemote("nope"); ok {
		t.Fatal("unknown client must not render")
	}
}

// TestRemoteGuideSnippetsAreCanonical: the remote snippet in each per-client
// guide is exactly the canonical render, and the Claude Code CLI form is too.
func TestRemoteGuideSnippetsAreCanonical(t *testing.T) {
	root := findRepoRoot(t)
	cases := []struct {
		relPath string
		marker  string
		want    string
	}{
		{"docs/examples/mcp-clients/claude-code.md", "claude-code-remote-json", RenderClaudeCodeRemoteJSON()},
		{"docs/examples/mcp-clients/claude-code.md", "claude-code-remote-add", RenderClaudeCodeRemoteAddCommand()},
		{"docs/examples/mcp-clients/codex.md", "codex-remote-toml", RenderCodexRemoteTOML()},
		{"docs/examples/mcp-clients/cursor.md", "cursor-remote-json", RenderCursorRemoteJSON()},
		{"docs/examples/mcp-clients/opencode.md", "opencode-remote-json", RenderOpenCodeRemoteJSON()},
	}
	for _, tc := range cases {
		t.Run(tc.marker, func(t *testing.T) {
			got, err := ExtractMarkedBlock(readDoc(t, root, tc.relPath), tc.marker)
			if err != nil {
				t.Fatal(err)
			}
			if got != strings.TrimRight(tc.want, "\n") {
				t.Fatalf("marker %q in %s drifted from its canonical render:\n%s", tc.marker, tc.relPath, got)
			}
		})
	}
}

// TestSidecarDocRemoteSectionNamesEveryRemoteFixture: the main sidecar doc's
// client table names each fixture file and each tool the remote flow uses.
func TestSidecarDocRemoteSectionNamesEveryRemoteFixture(t *testing.T) {
	root := findRepoRoot(t)
	doc := string(readDoc(t, root, "docs/mcp-sidecar.md"))
	start := strings.Index(doc, "## Remote (hosted) server")
	if start < 0 {
		t.Fatal("docs/mcp-sidecar.md has no Remote (hosted) server section")
	}
	end := strings.Index(doc[start+1:], "\n## ")
	section := doc[start : start+1+end]
	for _, f := range remoteFixtureFiles {
		if !strings.Contains(section, filepath.Base(f.path)) {
			t.Errorf("remote section does not name %s", f.path)
		}
	}
	for _, needle := range []string{
		"investigate_question", "investigation_result", "source_evidence", "context_for_task",
		"acr://guide/questions", "acr://guide/vocabulary", "acr://guide/conversation",
		"`investigate`", "`continue_investigation`", "`expand_evidence`",
		RemoteProtocolRevision, RemoteTokenEnvVar, "repository.slug",
	} {
		if !strings.Contains(section, needle) {
			t.Errorf("remote section lacks %q", needle)
		}
	}
	if regexp.MustCompile(`fcacr_[A-Za-z0-9_-]{20,}`).MatchString(section) {
		t.Error("remote section contains a token-shaped literal")
	}
}

// TestClientSkillsCoverTheInvestigationFlow: every packaged skill names the
// investigate -> result -> evidence tools, the receipt handoff, the remote
// scope requirement, and the untrusted-content rule. The STDIO package
// contract (mcp_commands, enabled tools) is unchanged and asserted by the
// package-tree tests.
func TestClientSkillsCoverTheInvestigationFlow(t *testing.T) {
	root := findRepoRoot(t)
	skills := []string{
		"clients/claude-code/marketplace/plugins/context-fabric/skills/context-fabric/SKILL.md",
		"clients/codex/marketplace/plugins/context-fabric/skills/context-fabric/SKILL.md",
		"clients/opencode/config/skills/context-fabric/SKILL.md",
		"clients/cursor/skills/context-fabric/SKILL.md",
	}
	for _, rel := range skills {
		t.Run(rel, func(t *testing.T) {
			text := string(readDoc(t, root, rel))
			for _, needle := range []string{
				"context_for_task", "source_evidence", "investigate_question", "investigation_result",
				"parent_result_id", "prior_*_receipts", "repository.slug", "acr://guide/", "untrusted",
			} {
				if !strings.Contains(text, needle) {
					t.Errorf("%s lacks %q", rel, needle)
				}
			}
		})
	}
}
