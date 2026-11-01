package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func grantedLines(t *testing.T, principal storage.Principal) []map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	app := &App{logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	app.logAccessGranted(context.Background(), principal, 0, 0, true, 3*time.Millisecond)
	var lines []map[string]any
	for _, entry := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(entry), &decoded); err != nil {
			t.Fatalf("decode %q: %v", entry, err)
		}
		lines = append(lines, decoded)
	}
	return lines
}

// CHAOS-12300 (design V5): a project-only admission writes its basis at Info,
// corpus-safe: the grant class, the project count and the digest class. No
// project id and no digest value reaches the line.
func TestAProjectOnlyAdmissionWritesItsBasisAtInfo(t *testing.T) {
	principal := storage.Principal{OrgID: "org_1", ClientClass: storage.CredentialClientClassOAuthUser,
		ProjectScopes: []string{"project.v2:linear:P-SECRET", "project.v2:linear:Q-SECRET"}}
	lines := grantedLines(t, principal)
	if len(lines) != 1 {
		t.Fatalf("%d lines, want 1: %v", len(lines), lines)
	}
	line := lines[0]
	if line["level"] != "INFO" || line["msg"] != "principal access read granted" || line["grant_class"] != "project_only" ||
		line["project_scope_count"] != float64(2) || line["grant_digest_class"] != "project" || line["decision"] != "granted" {
		t.Fatalf("granted line = %v, want one Info line with project_only, 2 projects, digest class project", line)
	}
	encoded, _ := json.Marshal(line)
	if strings.Contains(string(encoded), "SECRET") || strings.Contains(string(encoded), "p:") {
		t.Fatalf("the line carries an id or a digest value: %s", encoded)
	}
}

// Every other class keeps its Debug line, with its existing fields only.
func TestOtherGrantClassesKeepTheirDebugAdmissionLine(t *testing.T) {
	for _, principal := range []storage.Principal{
		{OrgID: "org_1"},
		{OrgID: "org_1", RepositoryScopes: []string{"*"}},
		{OrgID: "org_1", RepositoryScopes: []string{"acme/a"}},
	} {
		lines := grantedLines(t, principal)
		if len(lines) != 1 || lines[0]["level"] != "DEBUG" {
			t.Fatalf("%v: %v, want one Debug line", principal.RepositoryScopes, lines)
		}
		for _, key := range []string{"grant_class", "project_scope_count", "grant_digest_class"} {
			if _, ok := lines[0][key]; ok {
				t.Fatalf("%v: the Debug line gained %s: %v", principal.RepositoryScopes, key, lines[0])
			}
		}
	}
}
